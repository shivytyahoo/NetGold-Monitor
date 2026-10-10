package legacyimport

import (
	"fmt"
	"strings"
	"testing"
)

func TestDSNPipeAuthModes(t *testing.T) {
	pipe := `\\.\pipe\LOCALDB#1A2B3C4D\tsql\query`
	// Windows Authentication (no SQL login): trusted_connection, as
	// every version before v3.0.1 produced.
	d := ConnConfig{PipeName: pipe, Database: "master"}.DSN()
	if !strings.Contains(d, "server=np:"+pipe) || !strings.Contains(d, "trusted_connection=yes") {
		t.Errorf("pipe + Windows Auth DSN wrong: %s", d)
	}
	if strings.Contains(d, "user id=") {
		t.Errorf("Windows Auth DSN must not carry a login: %s", d)
	}
	// SQL login (v3.0.1, NetGold's own engine): user id + password,
	// and NO trusted_connection — this is the SSPI-free path.
	d = ConnConfig{PipeName: pipe, Database: "master", User: "netgold_import", Password: "Ab3!xYz9"}.DSN()
	if !strings.Contains(d, "user id=netgold_import") || !strings.Contains(d, "password=Ab3!xYz9") {
		t.Errorf("pipe + SQL login DSN wrong: %s", d)
	}
	if strings.Contains(d, "trusted_connection") {
		t.Errorf("SQL login DSN must not request a trusted connection: %s", d)
	}
}

func TestTargetAndAuthLabels(t *testing.T) {
	cases := []struct {
		cfg       ConnConfig
		target    string
		auth      string
		windowsOK bool
	}{
		{ConnConfig{PipeName: `\\.\pipe\LOCALDB#AB\tsql\query`}, "local SQL engine", "Windows Authentication", true},
		{ConnConfig{PipeName: `\\.\pipe\LOCALDB#AB\tsql\query`, User: "netgold_import", Password: "x"}, "local SQL engine", `SQL login "netgold_import"`, false},
		{ConnConfig{Server: "NIC", Instance: "LEGACY", WindowsAuth: true}, `SQL Server NIC\LEGACY`, "Windows Authentication", true},
		{ConnConfig{Server: "NIC", Port: 51433, WindowsAuth: true}, "SQL Server NIC,51433", "Windows Authentication", true},
		{ConnConfig{Server: "NIC", User: "sa", Password: "secret"}, "SQL Server NIC,1433", `SQL login "sa"`, false},
	}
	for _, c := range cases {
		if got := c.cfg.TargetLabel(); !strings.Contains(got, c.target) {
			t.Errorf("TargetLabel(%+v) = %q, want it to contain %q", c.cfg, got, c.target)
		}
		if got := c.cfg.AuthModeLabel(); got != c.auth {
			t.Errorf("AuthModeLabel(%+v) = %q, want %q", c.cfg, got, c.auth)
		}
		if got := c.cfg.UsesWindowsAuth(); got != c.windowsOK {
			t.Errorf("UsesWindowsAuth(%+v) = %v, want %v", c.cfg, got, c.windowsOK)
		}
		// Labels must never leak the password.
		if strings.Contains(c.cfg.TargetLabel()+c.cfg.AuthModeLabel(), "secret") {
			t.Errorf("labels leaked a password for %+v", c.cfg)
		}
	}
}

func TestIsSSPICredentialError(t *testing.T) {
	sspi := []string{
		"NextBytes InitializeSecurityContext failed 8009030e", // the exact field error
		"InitialBytes InitializeSecurityContext failed 8009030e",
		"SEC_E_NO_CREDENTIALS: no credentials",
		"failed: 0x8009030E (the logon session has no credentials)",
		"No credentials are available in the security package",
	}
	for _, s := range sspi {
		if !IsSSPICredentialError(fmt.Errorf("%s", s)) {
			t.Errorf("must classify as SSPI credential error: %q", s)
		}
	}
	notSSPI := []string{
		"dial tcp 127.0.0.1:1433: connectex: No connection could be made",
		"Login failed for user 'sa'.",
		"NextBytes something else failed 80090305", // a different SEC_E code
		"",
	}
	for _, s := range notSSPI {
		if IsSSPICredentialError(fmt.Errorf("%s", s)) {
			t.Errorf("must NOT classify as SSPI credential error: %q", s)
		}
	}
	if IsSSPICredentialError(nil) {
		t.Error("nil error must not classify")
	}
}

func TestFormatConnectError(t *testing.T) {
	raw := fmt.Errorf("NextBytes InitializeSecurityContext failed 8009030e")
	pipeWin := ConnConfig{PipeName: `\\.\pipe\LOCALDB#AB\tsql\query`, Database: "master"}

	// SSPI failure + Windows Auth + elevated session: target, mode,
	// raw error AND the session guidance with the tray remedy.
	got := FormatConnectError(pipeWin, raw, SessionContext{Elevated: true})
	for _, want := range []string{"connecting to the local SQL engine", "Windows Authentication",
		"8009030e", "administrator rights", "tray icon", "Start Menu", "dedicated SQL login"} {
		if !strings.Contains(got, want) {
			t.Errorf("elevated SSPI message must contain %q, got:\n%s", want, got)
		}
	}
	// SYSTEM/service session: named as such.
	got = FormatConnectError(pipeWin, raw, SessionContext{System: true})
	if !strings.Contains(got, "Windows service") {
		t.Errorf("service SSPI message must name the service session, got:\n%s", got)
	}
	// Plain session: still the guidance, without claiming elevation.
	got = FormatConnectError(pipeWin, raw, SessionContext{})
	if !strings.Contains(got, "no usable credentials") || strings.Contains(got, "administrator rights") {
		t.Errorf("plain-session SSPI message wrong:\n%s", got)
	}
	// SQL login config: an SSPI-looking error gets NO Windows-session
	// guidance (the login path does not use SSPI at all).
	sqlCfg := ConnConfig{PipeName: pipeWin.PipeName, User: "netgold_import", Password: "x"}
	got = FormatConnectError(sqlCfg, raw, SessionContext{Elevated: true})
	if strings.Contains(got, "tray icon") || !strings.Contains(got, `SQL login "netgold_import"`) {
		t.Errorf("SQL-login message must not carry SSPI guidance:\n%s", got)
	}
	// A non-SSPI failure under Windows Auth: target + mode + raw
	// error, no guidance block.
	got = FormatConnectError(ConnConfig{Server: "NIC", Instance: "LEGACY", WindowsAuth: true},
		fmt.Errorf("dial tcp: connection refused"), SessionContext{Elevated: true})
	for _, want := range []string{`SQL Server NIC\LEGACY`, "Windows Authentication", "connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain failure message must contain %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "tray icon") {
		t.Errorf("non-SSPI failure must not carry SSPI guidance:\n%s", got)
	}
}
