package legacyimport

// Backup-import server diagnosis + manual server override (v2.1.1).
//
// The original .bak flow reported every failure as "No SQL Server was
// found", which is wrong — and unhelpful — when a server IS installed
// but stopped, or running but refusing the sign-in. This file provides:
//
//   - ClassifyBakSituation: a pure classifier that turns a Detect()
//     candidate list into one of three precise situations, with the
//     per-candidate details attached for the UI.
//   - BakManualConn / ManualServerConn: the .bak card's optional
//     manual server override (server, authentication, user, password),
//     parsed and validated server-side.
//   - CheckManualServer: reachability + sign-in probe of a manually
//     entered server, returning the same structured error type.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BakSituationKind identifies which of the distinct "cannot open the
// backup file" situations applies.
type BakSituationKind string

const (
	// BakNoInstance: no SQL Server instance was found on this computer
	// at all (only the built-in default-port guess was tried).
	BakNoInstance BakSituationKind = "no_instance"
	// BakUnreachable: instance(s) were found, but none is answering.
	BakUnreachable BakSituationKind = "unreachable"
	// BakSignInFailed: a server answered, but the sign-in failed.
	BakSignInFailed BakSituationKind = "signin_failed"
	// BakImportFailed: a usable server was chosen, but the restore or
	// the import itself failed (v2.2.1 — these exits used to return an
	// unstructured error; every .bak failure now carries the shape).
	BakImportFailed BakSituationKind = "import_failed"
	// BakInvalidRequest: the request itself was rejected (for example
	// an unusable backup path) before any server was involved.
	BakInvalidRequest BakSituationKind = "invalid_request"
)

// StructuredBakError builds a BakImportError for a failure that is not
// one of the three classified detection situations, guaranteeing the
// candidates array is present (empty) rather than null, so the UI can
// rely on the shape for every .bak failure.
func StructuredBakError(kind BakSituationKind, message string) *BakImportError {
	return &BakImportError{Kind: kind, Message: message, Candidates: []BakCandidateInfo{}}
}

// BakCandidateInfo is one detected candidate, as shown to the user in
// the .bak error details (display name + what detection concluded).
type BakCandidateInfo struct {
	DisplayName string `json:"display_name"`
	Detail      string `json:"detail"`
}

// BakImportError is the structured error returned by the .bak import
// endpoint: a precise message, the situation kind, and the
// per-candidate details the UI renders under the message.
type BakImportError struct {
	Kind       BakSituationKind   `json:"error_kind"`
	Message    string             `json:"error"`
	Candidates []BakCandidateInfo `json:"candidates"`
}

func (e *BakImportError) Error() string { return e.Message }

func candidateInfos(cands []Candidate) []BakCandidateInfo {
	out := make([]BakCandidateInfo, 0, len(cands))
	for _, c := range cands {
		out = append(out, BakCandidateInfo{DisplayName: c.DisplayName, Detail: c.Detail})
	}
	return out
}

// ClassifyBakSituation turns a Detect() result into the precise
// situation for a .bak import that found no usable server. Pure
// function — unit-tested. (It is only called when no candidate could
// connect; a candidate that connects means the import proceeds.)
func ClassifyBakSituation(cands []Candidate) *BakImportError {
	infos := candidateInfos(cands)
	var found, answering []Candidate
	for _, c := range cands {
		if c.Source == "default" {
			continue // the synthetic 1433 guess is not a discovery
		}
		found = append(found, c)
		if c.Reachable && !c.CanConnect {
			answering = append(answering, c)
		}
	}
	names := func(list []Candidate) string {
		parts := make([]string, 0, len(list))
		for _, c := range list {
			parts = append(parts, c.DisplayName)
		}
		return strings.Join(parts, "; ")
	}
	switch {
	case len(answering) > 0:
		return &BakImportError{
			Kind: BakSignInFailed,
			Message: fmt.Sprintf("A SQL Server is running (%s) but NetGold could not sign in with Windows Authentication. "+
				"The legacy database account or a Windows admin account is needed. "+
				"You can enter SQL Server details in the Backup File card below, or use the Advanced form to import from the live database instead.",
				names(answering)),
			Candidates: infos,
		}
	case len(found) > 0:
		return &BakImportError{
			Kind: BakUnreachable,
			Message: fmt.Sprintf("SQL Server instance(s) were found on this computer (%s) but none is answering — is the SQL Server service running? "+
				"Start the service (for example via services.msc or SQL Server Configuration Manager), then try again. "+
				"Alternatively, enter the server details in the Backup File card below.",
				names(found)),
			Candidates: infos,
		}
	default:
		return &BakImportError{
			Kind:       BakNoInstance,
			Message:    "No SQL Server was found on this computer to open the backup file. A backup (.bak) file can only be opened by a SQL Server — start the SQL Server that came with the legacy system (or any local SQL Server), then try again.",
			Candidates: infos,
		}
	}
}

// BakManualConn is the .bak card's optional manual server override.
// When Server is filled in, the .bak import connects to that server
// instead of using auto-detection. The connection is built server-side
// only; nothing about it is stored.
type BakManualConn struct {
	Server   string `json:"server"`   // host, host\instance, host,port or host:port
	Database string `json:"database"` // accepted for symmetry; the restore always uses its own temporary database
	Auth     string `json:"auth"`     // "windows" or "sql" (empty means "windows")
	User     string `json:"user"`
	Password string `json:"password"`
}

// ParseManualServer splits a server string as typed by a user —
// "HOST", "HOST\INSTANCE", "HOST,1433" or "HOST:1433" (a trailing
// ",port" may follow an instance too) — into host, instance and port.
// Pure function — unit-tested.
func ParseManualServer(s string) (host, instance string, port int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", 0, fmt.Errorf("please enter the SQL Server")
	}
	rest := s
	// A trailing ",<digits>" is the port (also after an instance name).
	if i := strings.LastIndex(rest, ","); i >= 0 {
		if p, perr := strconv.Atoi(strings.TrimSpace(rest[i+1:])); perr == nil {
			if perr := checkPort(p); perr != nil {
				return "", "", 0, perr
			}
			port, rest = p, strings.TrimSpace(rest[:i])
		}
	}
	if i := strings.Index(rest, `\`); i >= 0 {
		host = strings.TrimSpace(rest[:i])
		instance = strings.TrimSpace(rest[i+1:])
		if host == "" || instance == "" {
			return "", "", 0, fmt.Errorf("the server should look like HOST\\INSTANCE, HOST,PORT or just HOST")
		}
		return host, instance, port, nil
	}
	if port == 0 {
		// host:port, when the part after the colon is numeric.
		if i := strings.LastIndex(rest, ":"); i >= 0 {
			if p, perr := strconv.Atoi(strings.TrimSpace(rest[i+1:])); perr == nil {
				if perr := checkPort(p); perr != nil {
					return "", "", 0, perr
				}
				host = strings.TrimSpace(rest[:i])
				return host, "", p, nil
			}
		}
	}
	if rest == "" {
		return "", "", 0, fmt.Errorf("please enter the SQL Server")
	}
	return rest, "", port, nil
}

func checkPort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("the port must be between 1 and 65535")
	}
	return nil
}

// ManualServerConn validates a BakManualConn and builds the connection
// configuration for it (pointed at the master database; the restore
// flow manages its own temporary database from there).
func ManualServerConn(m BakManualConn) (ConnConfig, error) {
	host, instance, port, err := ParseManualServer(m.Server)
	if err != nil {
		return ConnConfig{}, err
	}
	if d := strings.TrimSpace(m.Database); d != "" {
		// Accepted and checked for sanity, but not used: a .bak import
		// restores into its own temporary database, never into an
		// existing one.
		if strings.ContainsAny(d, "[];") {
			return ConnConfig{}, fmt.Errorf("the database name looks invalid")
		}
	}
	cfg := ConnConfig{Server: host, Instance: instance, Port: port, Database: "master"}
	switch strings.ToLower(strings.TrimSpace(m.Auth)) {
	case "", "windows":
		cfg.WindowsAuth = true
	case "sql":
		if strings.TrimSpace(m.User) == "" {
			return ConnConfig{}, fmt.Errorf("please enter the SQL login user name, or choose Windows Authentication")
		}
		cfg.User, cfg.Password = m.User, m.Password
	default:
		return ConnConfig{}, fmt.Errorf("the authentication type must be Windows or SQL Server")
	}
	return cfg, nil
}

// describeConn renders a connection config the way a user would type
// it, for use in messages.
func describeConn(cfg ConnConfig) string {
	s := cfg.Server
	if cfg.Instance != "" {
		s += `\` + cfg.Instance
	} else if cfg.Port > 0 {
		s += fmt.Sprintf(",%d", cfg.Port)
	}
	return s
}

// CheckManualServer probes a manually entered server before the
// restore is attempted: TCP reachability (when a port is known) and a
// real sign-in against the master database. It returns nil when the
// server is usable, or the structured situation otherwise.
func CheckManualServer(cfg ConnConfig) *BakImportError {
	desc := describeConn(cfg)
	if cfg.Instance == "" {
		port := cfg.Port
		if port == 0 {
			port = 1433
		}
		if !tcpReachable(cfg.Server, port, 3*time.Second) {
			return &BakImportError{
				Kind:       BakUnreachable,
				Message:    fmt.Sprintf("The SQL Server at %s is not answering on TCP port %d — is the SQL Server service running on that computer, and can this computer reach that port (firewall)? Check the server name and port, then try again.", desc, port),
				Candidates: []BakCandidateInfo{},
			}
		}
	}
	src, err := tryConnect(cfg, 8*time.Second)
	if err != nil {
		return &BakImportError{
			Kind: BakSignInFailed,
			Message: fmt.Sprintf("A SQL Server answered at %s, but NetGold could not sign in: %s. "+
				"Check the authentication choice and the login details — restoring a backup needs a login with restore permission (for example a sysadmin, or the legacy database owner). "+
				"If the instance uses a dynamic port and the SQL Browser service is stopped, enter the server as HOST,PORT instead of HOST\\INSTANCE.", desc, FormatConnectError(cfg, err, CurrentSessionContext())),
			Candidates: []BakCandidateInfo{},
		}
	}
	src.Close()
	return nil
}
