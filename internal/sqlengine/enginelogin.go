package sqlengine

// Dedicated SQL login for NetGold's own engine (v3.0.1).
//
// Why this exists: up to v3.0.0, imports through NetGold's own
// LocalDB instance connected over the instance pipe with Windows
// Authentication. That sign-in is an SSPI handshake made with the
// credentials of whatever Windows logon session the NetGold process
// happens to run in — and on real machines that session often has
// NO usable credentials (NetGold launched from the installer's
// finish page, running as a service/SYSTEM, or a user who signs in
// to Windows with a PIN / Windows Hello). The driver then fails
// with "NextBytes InitializeSecurityContext failed 8009030e"
// (SEC_E_NO_CREDENTIALS) before the restore can even start — while
// instance creation via SqlLocalDB.exe, which needs no SSPI, worked
// fine in the very same session.
//
// The fix removes that dependency for OUR engine only: when NetGold
// provisions its "NetGold" instance, it also provisions a dedicated
// SQL login on that instance (sysadmin there — the instance is a
// throwaway restore engine, owned by this user, on this machine).
// The login's password is generated on the machine, and stored ONLY
// in NetGold's data directory, protected with Windows DPAPI
// (per-user) — never logged, never sent to the browser, never
// leaving the machine. Imports through our own engine then use SQL
// authentication: no SSPI, immune to session quirks.
//
// Connections to PRE-EXISTING SQL Servers (a user's own legacy
// server) are unchanged: they keep Windows Authentication (or
// the login the user typed), exactly as before.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"netgold/internal/legacyimport"
)

// EngineLoginUser is the dedicated SQL login provisioned on
// NetGold's own LocalDB instance.
const EngineLoginUser = "netgold_import"

// Engine kinds recorded in the stored sign-in: which of NetGold's
// own engines the stored credentials belong to. A file written
// before v3.0.2 has no kind and means the LocalDB engine.
const (
	EngineKindLocalDB = "localdb"
	EngineKindExpress = "express"
)

// EngineLoginFile is the data-dir file holding the engine login:
// the user name plus the password as a DPAPI-protected blob
// (base64). The plaintext password is never written anywhere.
const EngineLoginFile = "engine-login.json"

// ---- platform hooks (defaults: unsupported; Windows replaces them) ----

var (
	// platProtectSecret encrypts bytes for storage (Windows: DPAPI,
	// per-user scope).
	platProtectSecret = func([]byte) ([]byte, error) { return nil, errUnsupported() }
	// platUnprotectSecret decrypts what platProtectSecret produced.
	platUnprotectSecret = func([]byte) ([]byte, error) { return nil, errUnsupported() }
	// platProvisionLoginHere creates/updates the engine login on the
	// instance at pipePath, signing in with Windows Authentication
	// from THIS process (the same context that created the instance
	// via SqlLocalDB.exe).
	platProvisionLoginHere = func(pipePath, user, password string) error { return errUnsupported() }
	// platRunProvisionChild re-runs the provisioning in the helper
	// child (netgold -engine-provision, elevated with one UAC
	// consent) for when this process's own sign-in session cannot
	// authenticate (the SSPI failure class).
	platRunProvisionChild = func(dataDir string, progress func(string)) error { return errUnsupported() }
	// platPingConn probes a connection config (open + ping + close).
	platPingConn = func(cfg legacyimport.ConnConfig) error { return legacyimport.Ping(cfg) }
	// platTightenLoginFile restricts the login file's ACL to the
	// current user (+ SYSTEM/Administrators). Best effort.
	platTightenLoginFile = func(path string) {}
)

// RunProvisionChild is the helper child's entry point, called by
// the program's main function when it is relaunched with
// -engine-provision. It provisions the engine login (this process
// was started with administrator approval in the user's session)
// and reports through files in the data directory. Implemented on
// Windows; other platforms return an error.
var RunProvisionChild = func(dataDir string) error {
	return fmt.Errorf("engine sign-in provisioning is available on Windows only")
}

// Provision file names inside the data directory: the helper child
// appends progress lines to ProvisionProgressFile and writes its
// final outcome to ProvisionResultFile — the same contract as the
// engine repair's files (see repair.go), parsed by the same code.
const (
	ProvisionProgressFile = "engine-provision-progress.txt"
	ProvisionResultFile   = "engine-provision-result.txt"
)

// ---- password generation (pure) ----

// Password character classes. The set deliberately excludes every
// character that is special in a connection string, a T-SQL literal
// or a URL (';', '=', quotes, braces, backslash, '/', '@', space),
// so the generated password can travel through all of them unescaped
// (it is still T-SQL-escaped when provisioned, as defence in depth).
const (
	pwLower   = "abcdefghijklmnopqrstuvwxyz"
	pwUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	pwDigits  = "0123456789"
	pwSymbols = "!#$%&*+-?^_"
	pwCharset = pwLower + pwUpper + pwDigits + pwSymbols
)

// enginePasswordLength is the generated password length: 32
// characters from a 73-character set ≈ 198 bits of entropy.
const enginePasswordLength = 32

// GenerateEnginePassword returns a fresh strong password for the
// engine login, using the operating system's random source.
func GenerateEnginePassword() (string, error) {
	return generateEnginePassword(rand.Reader)
}

// generateEnginePassword builds a password of enginePasswordLength
// characters with rejection sampling (no modulo bias), guaranteed
// to contain at least one character of every class. With a healthy
// random source the first attempt virtually always qualifies; a
// degenerate source errors out instead of looping forever.
func generateEnginePassword(r io.Reader) (string, error) {
	limit := byte(256 - (256 % len(pwCharset))) // largest multiple of the charset size that fits in a byte
	for attempt := 0; attempt < 100; attempt++ {
		var out []byte
		buf := make([]byte, 64)
		for len(out) < enginePasswordLength {
			if _, err := io.ReadFull(r, buf); err != nil {
				return "", fmt.Errorf("could not generate the engine password: %w", err)
			}
			for _, b := range buf {
				if b >= limit {
					continue
				}
				out = append(out, pwCharset[int(b)%len(pwCharset)])
				if len(out) == enginePasswordLength {
					break
				}
			}
		}
		pw := string(out)
		if hasAllClasses(pw) {
			return pw, nil
		}
	}
	return "", fmt.Errorf("could not generate a strong engine password")
}

func hasAllClasses(pw string) bool {
	var lo, up, di, sy bool
	for _, ch := range pw {
		switch {
		case strings.ContainsRune(pwLower, ch):
			lo = true
		case strings.ContainsRune(pwUpper, ch):
			up = true
		case strings.ContainsRune(pwDigits, ch):
			di = true
		case strings.ContainsRune(pwSymbols, ch):
			sy = true
		}
	}
	return lo && up && di && sy
}

// ---- provisioning T-SQL (pure) ----

// tsqlLiteral quotes a string as a T-SQL N'...' literal.
func tsqlLiteral(s string) string {
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// tsqlIdent quotes an identifier as [ ... ].
func tsqlIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

// provisionLoginSQL builds the idempotent batch that creates the
// engine login (or resets its password), enables it, and grants it
// sysadmin. It runs ONLY against NetGold's own throwaway LocalDB
// instance. CHECK_POLICY is off so the generated password is never
// subject to the machine's password policy or expiry. Pure
// function.
func provisionLoginSQL(user, password string) string {
	u := tsqlLiteral(user)
	ident := tsqlIdent(user)
	p := tsqlLiteral(password)
	return fmt.Sprintf(`IF NOT EXISTS (SELECT 1 FROM sys.server_principals WHERE name = %s)
    CREATE LOGIN %s WITH PASSWORD = %s, CHECK_POLICY = OFF, CHECK_EXPIRATION = OFF;
ELSE
    ALTER LOGIN %s WITH PASSWORD = %s, CHECK_POLICY = OFF, CHECK_EXPIRATION = OFF;
ALTER LOGIN %s ENABLE;
IF ISNULL(IS_SRVROLEMEMBER(N'sysadmin', %s), 0) <> 1
    ALTER SERVER ROLE [sysadmin] ADD MEMBER %s;`,
		u, ident, p, ident, p, ident, u, ident)
}

// ---- the protected login file (pure envelope + storage) ----

// engineLoginEnvelope is the on-disk JSON shape of EngineLoginFile:
// the login's user name and the DPAPI-protected password (base64 of
// the raw protected blob). Engine records which of NetGold's own
// engines the sign-in belongs to (EngineKindLocalDB when absent —
// files written before v3.0.2).
type engineLoginEnvelope struct {
	Version   int    `json:"version"`
	Engine    string `json:"engine,omitempty"`
	User      string `json:"user"`
	Protected string `json:"protected"`
}

// packEngineLogin serialises the envelope for the LocalDB engine.
// Pure function.
func packEngineLogin(user string, protected []byte) ([]byte, error) {
	return packEngineLoginFor(EngineKindLocalDB, user, protected)
}

// packEngineLoginFor serialises the envelope for one engine kind.
// Pure function.
func packEngineLoginFor(kind, user string, protected []byte) ([]byte, error) {
	return json.Marshal(engineLoginEnvelope{
		Version:   1,
		Engine:    kind,
		User:      user,
		Protected: base64.StdEncoding.EncodeToString(protected),
	})
}

// unpackEngineLogin parses the envelope. Pure function.
func unpackEngineLogin(data []byte) (user string, protected []byte, err error) {
	u, _, blob, e := unpackEngineLoginKind(data)
	return u, blob, e
}

// unpackEngineLoginKind parses the envelope, including the engine
// kind (EngineKindLocalDB for files that predate the field). Pure
// function.
func unpackEngineLoginKind(data []byte) (user, kind string, protected []byte, err error) {
	var env engineLoginEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", "", nil, fmt.Errorf("the stored engine sign-in is unreadable: %w", err)
	}
	if env.Version != 1 || env.User == "" || env.Protected == "" {
		return "", "", nil, fmt.Errorf("the stored engine sign-in is incomplete")
	}
	blob, err := base64.StdEncoding.DecodeString(env.Protected)
	if err != nil {
		return "", "", nil, fmt.Errorf("the stored engine sign-in is damaged: %w", err)
	}
	kind = env.Engine
	if kind == "" {
		kind = EngineKindLocalDB
	}
	return env.User, kind, blob, nil
}

// EngineLoginPath is where the login file lives for a data dir.
func EngineLoginPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, EngineLoginFile)
}

// LoadEngineLogin reads and decrypts the stored engine login.
// ok=false (with no usable credentials) whenever the file is
// missing, unreadable, or cannot be decrypted in this session —
// the caller then provisions a fresh login.
func LoadEngineLogin(dataDir string) (user, password string, ok bool) {
	user, password, _, ok = LoadStoredEngineLogin(dataDir)
	return user, password, ok
}

// LoadStoredEngineLogin is LoadEngineLogin plus the engine kind:
// which of NetGold's own engines the stored sign-in belongs to
// (EngineKindLocalDB or EngineKindExpress). The data directory
// holds ONE stored engine sign-in — the one the active engine
// uses; provisioning the other engine replaces it.
func LoadStoredEngineLogin(dataDir string) (user, password, kind string, ok bool) {
	path := EngineLoginPath(dataDir)
	if path == "" {
		return "", "", "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", false
	}
	user, kind, blob, err := unpackEngineLoginKind(data)
	if err != nil {
		return "", "", "", false
	}
	plain, err := platUnprotectSecret(blob)
	if err != nil || len(plain) == 0 {
		return "", "", "", false
	}
	return user, string(plain), kind, true
}

// SaveEngineLogin protects the password and stores the envelope in
// the data directory (file permissions restricted, and the ACL
// tightened on Windows). The plaintext password is never written.
// The stored kind is the LocalDB engine (v3.0.1 behaviour).
func SaveEngineLogin(dataDir, user, password string) error {
	return saveEngineLogin(dataDir, EngineKindLocalDB, user, password)
}

// SaveExpressLogin stores the SQL Server Express engine's 'sa'
// password, protected exactly like the LocalDB engine's login
// (DPAPI per-user, same file, engine kind recorded). Called by the
// elevated Express setup helper after it installs the instance
// with this password.
func SaveExpressLogin(dataDir, password string) error {
	return saveEngineLogin(dataDir, EngineKindExpress, ExpressLoginUser, password)
}

func saveEngineLogin(dataDir, kind, user, password string) error {
	path := EngineLoginPath(dataDir)
	if path == "" {
		return fmt.Errorf("no data directory is configured for the engine sign-in")
	}
	blob, err := platProtectSecret([]byte(password))
	if err != nil {
		return fmt.Errorf("could not protect the engine password for storage: %w", err)
	}
	data, err := packEngineLoginFor(kind, user, blob)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("could not store the engine sign-in: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	platTightenLoginFile(path)
	return nil
}

// icaclsGrantArgs builds the icacls invocation that restricts a
// file to the given user SID plus SYSTEM and the Administrators
// group (well-known SIDs, language-independent). Pure function.
func icaclsGrantArgs(path, userSID string) []string {
	return []string{path, "/inheritance:r",
		"/grant:r", "*" + userSID + ":F",
		"/grant:r", "*S-1-5-18:F",
		"/grant:r", "*S-1-5-32-544:F"}
}

// ---- orchestration ----

// ownEngineConn resolves how imports sign in to NetGold's own
// LocalDB instance at pipePath: with the provisioned SQL login,
// never with Windows Authentication. An existing stored login that
// still signs in is reused; otherwise a fresh login is provisioned
// — in this process when its sign-in works, or through the helper
// child (one administrator approval) when this process's session
// hits the SSPI credential failure.
func (s *Setup) ownEngineConn(pipe string) (legacyimport.ConnConfig, error) {
	pipePath := PipePathFromInfo(pipe)
	dataDir := s.dataDir()
	if dataDir == "" {
		return legacyimport.ConnConfig{}, fmt.Errorf("NetGold has no data directory configured, so the engine sign-in cannot be stored")
	}
	mkCfg := func(user, password string) legacyimport.ConnConfig {
		return legacyimport.ConnConfig{PipeName: pipePath, Database: "master", User: user, Password: password}
	}
	// Fast path: a stored login that still signs in.
	if user, pw, ok := LoadEngineLogin(dataDir); ok {
		if cfg := mkCfg(user, pw); platPingConn(cfg) == nil {
			return cfg, nil
		}
		// Stored but no longer valid (the instance was removed and
		// recreated, so the login went with it): re-provision below.
	}
	// Provision in this process: the connection is made in the same
	// user context that created the instance via SqlLocalDB.exe.
	pw, err := GenerateEnginePassword()
	if err != nil {
		return legacyimport.ConnConfig{}, err
	}
	provErr := platProvisionLoginHere(pipePath, EngineLoginUser, pw)
	if provErr == nil {
		if err := SaveEngineLogin(dataDir, EngineLoginUser, pw); err != nil {
			return legacyimport.ConnConfig{}, err
		}
		cfg := mkCfg(EngineLoginUser, pw)
		if err := platPingConn(cfg); err != nil {
			return legacyimport.ConnConfig{}, fmt.Errorf("the engine sign-in NetGold just created could not be verified: %v", err)
		}
		s.addNote("NetGold's own engine now signs in with a dedicated SQL login stored protected on this computer, so imports no longer depend on Windows sign-in credentials.")
		return cfg, nil
	}
	if !legacyimport.IsSSPICredentialError(provErr) {
		return legacyimport.ConnConfig{}, fmt.Errorf("creating the engine's dedicated sign-in failed: %v", provErr)
	}
	// This process's sign-in session has no usable Windows
	// credentials (the 8009030e situation): hand the provisioning to
	// the helper child, started fresh with administrator approval.
	s.setDetail("Preparing the engine sign-in (administrator approval needed)…")
	if err := platRunProvisionChild(dataDir, s.setDetail); err != nil {
		return legacyimport.ConnConfig{}, err
	}
	user, pw2, ok := LoadEngineLogin(dataDir)
	if !ok {
		return legacyimport.ConnConfig{}, fmt.Errorf("the sign-in helper finished, but the engine sign-in it stored cannot be read in this session — start NetGold normally from the Start Menu (not via the installer) and try the import again")
	}
	cfg := mkCfg(user, pw2)
	if err := platPingConn(cfg); err != nil {
		return legacyimport.ConnConfig{}, fmt.Errorf("the engine sign-in stored by the helper could not be verified: %v", err)
	}
	s.addNote("NetGold's own engine now signs in with a dedicated SQL login stored protected on this computer, so imports no longer depend on Windows sign-in credentials.")
	return cfg, nil
}
