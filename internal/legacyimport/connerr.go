package legacyimport

// Connection-failure diagnosis (v3.0.1).
//
// A bare driver error such as "NextBytes InitializeSecurityContext
// failed 8009030e" tells the user nothing: not WHICH server NetGold
// was talking to, not HOW it tried to sign in, and not what to do
// about it. This file provides the pure pieces that turn a raw
// connection failure into a precise sentence:
//
//   - TargetLabel / AuthModeLabel describe the connection (the
//     password is never part of either — targets and modes only).
//   - IsSSPICredentialError recognises the Windows Authentication
//     "no credentials in this sign-in session" failure class
//     (SEC_E_NO_CREDENTIALS, 0x8009030e).
//   - FormatConnectError renders the full message, including — for
//     that SSPI class under Windows Authentication — the session
//     guidance: the sign-in session NetGold is running in has no
//     usable Windows credentials (typical when NetGold was started
//     by the installer, runs as a service/SYSTEM, or the user signs
//     in to Windows with a PIN / Windows Hello), and what to do.

import (
	"fmt"
	"strings"
)

// SessionContext describes the Windows session the NetGold process
// is running in, as far as connection diagnosis needs it. On
// non-Windows platforms both flags are always false.
type SessionContext struct {
	// Elevated: the process holds an administrator (elevated) token.
	Elevated bool
	// System: the process runs as the SYSTEM account (the usual
	// Windows service identity).
	System bool
}

// platSessionContext is the platform hook behind
// CurrentSessionContext (session_windows.go / session_other.go).
var platSessionContext = func() SessionContext { return SessionContext{} }

// CurrentSessionContext reports this process's session context.
func CurrentSessionContext() SessionContext { return platSessionContext() }

// UsesWindowsAuth reports whether this configuration signs in with
// Windows Authentication (SSPI): explicitly requested, or implied
// by a named-pipe target with no SQL login (the LocalDB engine
// setup's original mode).
func (c ConnConfig) UsesWindowsAuth() bool {
	if c.User != "" {
		return false
	}
	return c.WindowsAuth || c.PipeName != ""
}

// TargetLabel renders the connection target the way a user would
// recognise it: the server/instance or port, or — for a named pipe —
// the local engine and the pipe path. It never contains credentials
// (a pipe path names an instance, not a secret).
func (c ConnConfig) TargetLabel() string {
	if c.PipeName != "" {
		return fmt.Sprintf("the local SQL engine (NetGold's own LocalDB instance, pipe %s)", c.PipeName)
	}
	s := c.Server
	if s == "" {
		s = "localhost"
	}
	if c.Instance != "" {
		return fmt.Sprintf("SQL Server %s\\%s", s, c.Instance)
	}
	port := c.Port
	if port == 0 {
		port = 1433
	}
	return fmt.Sprintf("SQL Server %s,%d", s, port)
}

// AuthModeLabel renders how the connection signs in: "Windows
// Authentication", or the SQL login's user name (never its
// password).
func (c ConnConfig) AuthModeLabel() string {
	if c.User != "" {
		return fmt.Sprintf("SQL login %q", c.User)
	}
	return "Windows Authentication"
}

// IsSSPICredentialError reports whether err is the SSPI "no usable
// credentials in this logon session" failure (SEC_E_NO_CREDENTIALS,
// 0x8009030e) as text: the go-mssqldb winsspi provider reports it as
// "NextBytes InitializeSecurityContext failed 8009030e". Pure
// function — matches on the error text only.
func IsSSPICredentialError(err error) bool {
	if err == nil {
		return false
	}
	t := strings.ToLower(err.Error())
	return strings.Contains(t, "8009030e") ||
		strings.Contains(t, "sec_e_no_credentials") ||
		strings.Contains(t, "initializesecuritycontext") ||
		strings.Contains(t, "no credentials are available in the security package")
}

// sspiGuidance is the plain-English explanation + remedy appended
// to an SSPI credential failure under Windows Authentication. The
// first sentence adapts to what was detected about this process's
// session; the remedy is the same in both cases.
func sspiGuidance(sess SessionContext) string {
	var first string
	switch {
	case sess.System:
		first = "NetGold is running as a Windows service (SYSTEM account), and that account has no Windows sign-in credentials SQL Server can use."
	case sess.Elevated:
		first = "NetGold is running with administrator rights, in a Windows sign-in session that has no usable credentials for SQL Server."
	default:
		first = "This Windows sign-in session has no usable credentials for SQL Server."
	}
	return first + " This often happens when NetGold was started by the installer, or when you sign in to Windows with a PIN or Windows Hello instead of a password. " +
		"To fix it: sign out of Windows and sign back in once with your account password (not a PIN or Windows Hello) — a password sign-in restores the credentials this session is missing — then try again. " +
		"If NetGold was started by the installer, exiting it from its tray icon and starting it again normally from the Start Menu can also help. " +
		"Or let NetGold set up its own engine (the \"Set Up Engine and Import\" button): its engines sign in with a dedicated SQL login, so this problem cannot happen there."
}

// FormatConnectError renders a connection failure for the user: the
// target that was tried, the authentication mode, the underlying
// error, and — for an SSPI credential failure under Windows
// Authentication — the session guidance. Pure function.
func FormatConnectError(cfg ConnConfig, err error, sess SessionContext) string {
	msg := fmt.Sprintf("connecting to %s (%s): %v", cfg.TargetLabel(), cfg.AuthModeLabel(), err)
	if cfg.UsesWindowsAuth() && IsSSPICredentialError(err) {
		msg += "\n" + sspiGuidance(sess)
	}
	return msg
}
