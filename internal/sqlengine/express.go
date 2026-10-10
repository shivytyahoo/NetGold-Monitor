package sqlengine

// SQL Server Express engine (v3.0.2) — the primary engine path when
// Windows Authentication (SSPI) cannot be used at all.
//
// What v3.0.1's field failure proved: provisioning the dedicated
// SQL login on the LocalDB engine itself needs a first sysadmin
// connection, and on LocalDB the only initial sysadmin path is the
// owner's SSPI sign-in. In a sign-in session whose SSPI has no
// usable credentials (the classic case is signing in to Windows
// with a PIN or Windows Hello: the session holds no password
// credential for NTLM to offer, error 8009030e — and an elevated
// token from the same session shares the problem), that first
// connection can never be made, so the login can never be created
// and the LocalDB path is stuck no matter how clean the install is.
//
// The Express path engineers that dependency away. SQL Server
// Express installs as a Windows SERVICE via Microsoft's installer
// bootstrapper, with mixed-mode security and a generated 'sa'
// password chosen by NetGold at install time (/SECURITYMODE=SQL
// /SAPWD=...): the 'sa' SQL login exists and works the moment the
// setup finishes, and installing a service never involves an SSPI
// handshake. NetGold pins the instance to a static TCP port, so
// imports connect to 127.0.0.1:<port> with plain SQL
// authentication — no SSPI, no named pipes, no dependency on the
// sign-in session, the elevation state, or PIN-vs-password sign-in.
// The whole provisioning runs in the elevated helper child
// (netgold -engine-express, one UAC consent), like the repair.
//
// Which installer: Microsoft retired the SQL Server 2022 Express
// download (its Download Center page and the 2022 bootstrapper
// file both answer 404 as of October 2026), and no direct
// full-media .exe is published for Express — the official delivery
// is the small installer bootstrapper (SSEI), which downloads the
// engine media from Microsoft itself during setup. NetGold
// therefore pins the CURRENT official bootstrapper — SQL Server
// 2025 Express — below: the parent downloads it, checks its exact
// size, and verifies its Authenticode signature (Valid, signed by
// Microsoft) before the elevated helper ever runs it. Verified
// live from the build sandbox on 2026-10-09: HTTP 200, exactly
// 4,606,792 bytes, PE FileVersion 17.0.1000.9, embedded signature
// chain "Microsoft Code Signing PCA 2024" -> leaf signer
// "CN=Microsoft Corporation".

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"netgold/internal/legacyimport"
)

// ExpressDownloadURL is the official SQL Server 2025 Express
// installer bootstrapper (SQL2025-SSEI-Expr.exe) on
// download.microsoft.com — the delivery Microsoft's own SQL Server
// downloads page links to for Express (via aka.ms/sql2025express).
const ExpressDownloadURL = "https://download.microsoft.com/download/ffd82b4c-9955-47c0-8efe-6290f7795cf6/SQL2025-SSEI-Expr.exe"

// ExpressDownloadBytes is the verified size of the bootstrapper
// above (see the package comment for how it was verified).
const ExpressDownloadBytes = 4606792

// ExpressInstanceName is the dedicated named instance NetGold
// installs ("<machine>\NETGOLD", service MSSQL$NETGOLD). It is
// deliberately not SQLEXPRESS, so it never collides with an
// instance the user installed themselves.
const ExpressInstanceName = "NETGOLD"

// ExpressPort is the static TCP port the NETGOLD instance is
// configured to listen on (127.0.0.1). A fixed port means imports
// never need the SQL Browser service or dynamic-port discovery.
const ExpressPort = 11433

// ExpressLoginUser is the administrator SQL login the Express
// setup creates: the built-in 'sa', whose password NetGold
// generates at install time (see SaveExpressLogin).
const ExpressLoginUser = "sa"

// ExpressProgressFile / ExpressResultFile are the data-dir files
// the elevated Express helper reports through — the same contract
// as the repair and sign-in helpers (see repair.go).
const (
	ExpressProgressFile = "engine-express-progress.txt"
	ExpressResultFile   = "engine-express-result.txt"
)

// ---- platform hooks (defaults: unsupported; Windows replaces them) ----

var (
	// platFindExpress reports whether the NETGOLD Express instance's
	// service exists on this machine.
	platFindExpress = func() bool { return false }
	// platProbeSSPI makes one short Windows Authentication trial
	// connection to the LocalDB instance at pipePath and returns
	// its error (nil = the session's SSPI sign-in works).
	platProbeSSPI = func(pipePath string) error { return fmt.Errorf("the sign-in check is available on Windows only") }
	// platProvisionExpress runs the elevated Express setup helper
	// (install or verify the NETGOLD instance) for the verified
	// bootstrapper at installerPath.
	platProvisionExpress = func(dataDir, installerPath string, progress func(string)) error { return errUnsupported() }
)

// RunExpressChild is the helper child's entry point, called by the
// program's main function when it is relaunched with
// -engine-express. Implemented on Windows; other platforms return
// an error.
var RunExpressChild = func(dataDir, installerPath string) error {
	return fmt.Errorf("SQL Server Express setup is available on Windows only")
}

// downloadExpress fetches the Express bootstrapper (a variable for
// testability; production downloads from Microsoft).
var downloadExpress = func(ctx context.Context, dest string, progress func(int)) error {
	return downloadFile(ctx, ExpressDownloadURL, dest, progress)
}

// ---- pure planning (unit-tested) ----

// expressServiceName is the Windows service name of a named SQL
// Server instance. Pure function.
func expressServiceName(instance string) string {
	return "MSSQL$" + instance
}

// superSocketNetLibKey is the registry key holding an instance's
// TCP listener settings, given the instance ID the setup recorded
// (e.g. "MSSQL17.NETGOLD" for SQL Server 2025). Pure function.
func superSocketNetLibKey(instanceID string) string {
	return `SOFTWARE\Microsoft\Microsoft SQL Server\` + instanceID + `\MSSQLServer\SuperSocketNetLib\Tcp\IPAll`
}

// regStringValue is one planned REG_SZ registry value.
type regStringValue struct {
	Name  string
	Value string
}

// expressTCPPlan is the listener configuration the helper writes
// after setup: a static TcpPort and TcpDynamicPorts cleared (an
// empty string is what disables dynamic ports). Pure function.
func expressTCPPlan(port int) []regStringValue {
	return []regStringValue{
		{Name: "TcpPort", Value: strconv.Itoa(port)},
		{Name: "TcpDynamicPorts", Value: ""},
	}
}

// expressInstanceDir is the NetGold-owned folder the Express
// instance's files live under (the setup's INSTANCEDIR and
// INSTALLSQLDATADIR), beneath the machine-wide application data
// folder. Pure function.
func expressInstanceDir(commonAppData string) string {
	return filepath.Join(commonAppData, "NetGold", "SQLEngine")
}

// expressSSEIArgs builds the bootstrapper command line for a quiet
// install. The bootstrapper consumes its own switches (/Q,
// /ACTION, /IACCEPTSQLSERVERLICENSETERMS, /HideProgressBar) and
// forwards the setup switches to SQL Server setup. SECURITYMODE=SQL
// plus SAPWD is what makes a working 'sa' login exist the moment
// setup finishes — no SSPI is involved in creating it. The
// generated password's character set excludes quotes and ';', so
// it travels through the command line unescaped. Pure function.
func expressSSEIArgs(instance, saPassword, instanceDir string) []string {
	return []string{
		"/Q",
		"/ACTION=Install",
		"/IACCEPTSQLSERVERLICENSETERMS",
		"/HideProgressBar",
		"/INSTANCENAME=" + instance,
		"/FEATURES=SQLEngine",
		"/SECURITYMODE=SQL",
		"/SAPWD=" + saPassword,
		"/ADDCURRENTUSERASSQLADMIN=true",
		"/TCPENABLED=1",
		"/BROWSERSVCSTARTUPTYPE=Disabled",
		"/SQLSVCSTARTUPTYPE=Automatic",
		"/UpdateEnabled=False",
		"/INSTANCEDIR=" + instanceDir,
		"/INSTALLSQLDATADIR=" + instanceDir,
	}
}

// expressExitGuidance maps the installer bootstrapper's exit code
// (which mirrors SQL Server setup's) to (fatal, message): 0 is a
// plain success, 3010 (restart required) and 1641 (restart
// initiated) are successes with a note. Pure function.
func expressExitGuidance(code uint32) (fatal bool, message string) {
	switch code {
	case 0:
		return false, ""
	case 3010:
		return false, "The SQL Server Express installer asks for a Windows restart to finish completely (exit code 3010); the engine is installed and usable now, and a restart is recommended when convenient."
	case 1641:
		return false, "The SQL Server Express installer has started a Windows restart to finish (exit code 1641); the engine is installed — if the import does not continue after the restart, just run it again."
	default:
		return true, fmt.Sprintf("The SQL Server Express installer exited with code %d (0x%08X). The SQL Server setup log (Summary.txt, in the SQL Server Setup Bootstrap log folder) records the installer's own explanation.", code, code)
	}
}

// ---- the sign-in (SSPI) probe ----

// sspiProbeOutcome classifies the trial Windows Authentication
// connection to the LocalDB engine.
type sspiProbeOutcome int

const (
	// probeNotRun: no LocalDB engine to probe (or it would not start).
	probeNotRun sspiProbeOutcome = iota
	// probeOK: the session's SSPI sign-in works.
	probeOK
	// probeSSPIBroken: the trial failed with the no-credentials
	// class (8009030e) — this session cannot use Windows
	// Authentication, so nothing that depends on it can be
	// provisioned either.
	probeSSPIBroken
	// probeInconclusive: the trial failed some other way (timeout,
	// instance still starting, ...); the LocalDB path is not ruled
	// in, but not proven impossible either.
	probeInconclusive
)

// classifySSPIProbe turns the trial connection's error into the
// outcome. Pure function.
func classifySSPIProbe(err error) sspiProbeOutcome {
	if err == nil {
		return probeOK
	}
	if legacyimport.IsSSPICredentialError(err) {
		return probeSSPIBroken
	}
	return probeInconclusive
}

// ---- engine path selection (pure, unit-tested) ----

// engineFacts is what the setup job has established about this
// machine before choosing an engine path.
type engineFacts struct {
	// StoredWorks: the stored engine sign-in (whichever engine it
	// belongs to) still signs in — nothing to set up at all.
	StoredWorks bool
	// LocalDBPresent: the LocalDB engine binaries are installed
	// (the NetGold instance may still need creating/starting).
	LocalDBPresent bool
	// Probe: the SSPI trial result against the LocalDB engine.
	Probe sspiProbeOutcome
}

// enginePath is the chosen route to a working engine.
type enginePath int

const (
	// pathStored: reuse the stored engine sign-in.
	pathStored enginePath = iota
	// pathLocalDB: the v3.0.1 LocalDB route (instance + provisioned
	// SQL login) — viable only when the session's SSPI sign-in
	// works, because provisioning itself needs that first SSPI
	// connection.
	pathLocalDB
	// pathExpress: the SQL Server Express service route — no SSPI
	// dependency anywhere.
	pathExpress
)

// chooseEnginePath is the selection rule, in order: a stored
// sign-in that still works wins; LocalDB only when its SSPI probe
// succeeded; otherwise Express. Pure function.
func chooseEnginePath(f engineFacts) enginePath {
	if f.StoredWorks {
		return pathStored
	}
	if f.LocalDBPresent && f.Probe == probeOK {
		return pathLocalDB
	}
	return pathExpress
}

// fallbackAction is what to do when the Express path has failed.
type fallbackAction int

const (
	// fallbackLocalDB: still try the LocalDB route (the probe did
	// not prove its sign-in impossible).
	fallbackLocalDB fallbackAction = iota
	// fallbackTerminal: no engine path remains — report, with the
	// password sign-in remedy.
	fallbackTerminal
)

// fallbackAfterExpressFailure decides the fallback from the probe
// outcome: when the probe proved the session's SSPI credentials
// are missing (or no probe could run), the LocalDB route cannot be
// provisioned either, so the honest end state is the terminal
// guidance; when the probe merely failed inconclusively (or even
// succeeded — the stored check runs earlier), LocalDB is still
// worth the attempt. Pure function.
func fallbackAfterExpressFailure(probe sspiProbeOutcome) fallbackAction {
	if probe == probeOK || probe == probeInconclusive {
		return fallbackLocalDB
	}
	return fallbackTerminal
}

// passwordSignInRemedy is the field-proven remedy for a sign-in
// session without usable SSPI credentials, in plain language: one
// password sign-in restores the credentials a PIN/Windows Hello
// session lacks, and one successful provisioning then lasts for
// every future import (the provisioned sign-in is stored).
const passwordSignInRemedy = "The most likely cause is that this Windows sign-in session has no usable credentials for SQL Server (error 8009030e), which happens when you sign in to Windows with a PIN or Windows Hello instead of a password. " +
	"The remedy: sign out of Windows and sign back in once with your account password (not the PIN or Windows Hello), then run this import again. " +
	"A password sign-in restores the missing credentials — and once an engine sign-in has been provisioned successfully a single time, NetGold stores it protected on this computer, so future imports no longer depend on it."

// terminalEngineError renders the end state when every engine path
// failed: what was tried, then the remedy. Pure function.
func terminalEngineError(tried []string) string {
	msg := "NetGold could not set up a working SQL engine on this computer."
	if len(tried) > 0 {
		msg += " What was tried: " + strings.Join(tried, "; ") + "."
	}
	return msg + " " + passwordSignInRemedy
}

// ---- orchestration helpers (used by the setup job) ----

// expressConnConfig is the connection imports use against the
// NETGOLD Express instance: loopback TCP on the static port, SQL
// authentication. Pure function.
func expressConnConfig(user, password string) legacyimport.ConnConfig {
	return legacyimport.ConnConfig{
		Server:   "127.0.0.1",
		Port:     ExpressPort,
		Database: "master",
		User:     user,
		Password: password,
	}
}

// storedEngineConn resolves a stored engine sign-in that still
// works, for whichever engine it belongs to: (a) of the path
// selection. For Express it is a plain TCP ping; for LocalDB the
// instance is started first and the sign-in is tried over its pipe
// (SQL authentication — no SSPI needed to USE a stored login; only
// creating one needs it).
func (s *Setup) storedEngineConn() (legacyimport.ConnConfig, bool) {
	dataDir := s.dataDir()
	if dataDir == "" {
		return legacyimport.ConnConfig{}, false
	}
	user, pw, kind, ok := LoadStoredEngineLogin(dataDir)
	if !ok {
		return legacyimport.ConnConfig{}, false
	}
	if kind == EngineKindExpress {
		cfg := expressConnConfig(user, pw)
		if platPingConn(cfg) == nil {
			return cfg, true
		}
		return legacyimport.ConnConfig{}, false
	}
	exe, found := platFindLocalDB()
	if !found {
		return legacyimport.ConnConfig{}, false
	}
	pipe, err := platEnsureInstance(exe)
	if err != nil {
		return legacyimport.ConnConfig{}, false
	}
	cfg := legacyimport.ConnConfig{PipeName: PipePathFromInfo(pipe), Database: "master", User: user, Password: pw}
	if platPingConn(cfg) == nil {
		return cfg, true
	}
	return legacyimport.ConnConfig{}, false
}

// runExpressImport executes the Express path inside the setup job:
// download the bootstrapper (with progress), verify its Microsoft
// signature, run the elevated provisioning helper, then finish
// the import through the new engine. A nil return means the job
// was completed here; a non-nil error is the attempt's failure,
// for the caller's fallback decision.
func (s *Setup) runExpressImport(bakPath string) error {
	s.setStep(StepDownloading)
	dir, err := os.MkdirTemp("", "netgold-express-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dlPath := filepath.Join(dir, "SQL2025-SSEI-Expr.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	dlErr := downloadExpress(ctx, dlPath, s.setProgress)
	cancel()
	if dlErr != nil {
		return fmt.Errorf("downloading the SQL Server Express installer from Microsoft failed: %v", dlErr)
	}
	if fi, err := os.Stat(dlPath); err != nil || fi.Size() != ExpressDownloadBytes {
		got := int64(-1)
		if err == nil {
			got = fi.Size()
		}
		return fmt.Errorf("the downloaded SQL Server Express installer has the wrong size (%d bytes, expected %d) — it was NOT run", got, ExpressDownloadBytes)
	}
	s.setStep(StepVerifying)
	if err := platVerifySignature(dlPath); err != nil {
		return err
	}
	s.setStep(StepInstalling)
	s.setDetail("Installing the SQL Server Express engine — Microsoft's installer downloads the engine files itself, so this step can take several minutes…")
	if err := platProvisionExpress(s.dataDir(), dlPath, s.setDetail); err != nil {
		return err
	}
	s.setDetail("")
	user, pw, kind, ok := LoadStoredEngineLogin(s.dataDir())
	if !ok || kind != EngineKindExpress {
		return fmt.Errorf("the Express setup finished, but the engine sign-in it stored cannot be read in this session — start NetGold normally from the Start Menu (not via the installer) and try the import again")
	}
	cfg := expressConnConfig(user, pw)
	if err := platPingConn(cfg); err != nil {
		return fmt.Errorf("the Express engine is installed, but NetGold could not sign in to it: %v", err)
	}
	s.addNote("NetGold set up Microsoft SQL Server Express (instance " + ExpressInstanceName + ") as its backup-import engine, with its own protected sign-in, so imports do not depend on Windows sign-in credentials. The Express engine stays installed as a Windows service so future backup imports work right away.")
	s.finishImport(cfg, bakPath)
	return nil
}
