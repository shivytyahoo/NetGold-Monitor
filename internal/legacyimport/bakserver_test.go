package legacyimport

import (
	"strings"
	"testing"
)

func TestClassifyBakSituationNoInstance(t *testing.T) {
	// Nothing detected at all.
	e := ClassifyBakSituation(nil)
	if e.Kind != BakNoInstance {
		t.Fatalf("empty list: kind = %q, want %q", e.Kind, BakNoInstance)
	}
	if !strings.Contains(e.Message, "No SQL Server was found on this computer") {
		t.Errorf("empty list message: %s", e.Message)
	}
	// Only the synthetic default-port guess, not answering: still the
	// "nothing found" situation, not "found but stopped".
	e = ClassifyBakSituation([]Candidate{
		{DisplayName: "This computer — SQL Server (default instance, port 1433)", Source: "default", Detail: "No SQL Server answered on this port."},
	})
	if e.Kind != BakNoInstance {
		t.Errorf("default-only list: kind = %q, want %q", e.Kind, BakNoInstance)
	}
	if len(e.Candidates) != 1 || e.Candidates[0].Detail == "" {
		t.Errorf("candidate details must be carried through: %+v", e.Candidates)
	}
}

func TestClassifyBakSituationUnreachable(t *testing.T) {
	e := ClassifyBakSituation([]Candidate{
		{DisplayName: "This computer — instance LEGACY", Source: "registry", Detail: "Not reachable."},
		{DisplayName: "This computer — SQL Server (default instance, port 1433)", Source: "default", Detail: "No SQL Server answered on this port."},
	})
	if e.Kind != BakUnreachable {
		t.Fatalf("kind = %q, want %q", e.Kind, BakUnreachable)
	}
	if !strings.Contains(e.Message, "LEGACY") || !strings.Contains(e.Message, "none is answering") ||
		!strings.Contains(e.Message, "is the SQL Server service running?") {
		t.Errorf("message must name the instance and ask about the service: %s", e.Message)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("all candidates must be included for the UI: %+v", e.Candidates)
	}
}

func TestClassifyBakSituationSignInFailed(t *testing.T) {
	e := ClassifyBakSituation([]Candidate{
		{DisplayName: "This computer — instance LEGACY (port 51433)", Source: "registry-port",
			Reachable: true, Detail: "Server found, but sign-in failed — database not accessible without credentials. Use the Advanced manual connection below."},
		{DisplayName: "This computer — SQL Server (default instance, port 1433)", Source: "default"},
	})
	if e.Kind != BakSignInFailed {
		t.Fatalf("kind = %q, want %q", e.Kind, BakSignInFailed)
	}
	for _, want := range []string{"LEGACY", "51433", "could not sign in with Windows Authentication",
		"Backup File card", "Advanced form"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message must contain %q: %s", want, e.Message)
		}
	}
}

func TestClassifyBakSituationDisabledInstance(t *testing.T) {
	// A TCP-disabled instance is "found but not answering", and its
	// specific detail line rides along for the UI.
	e := ClassifyBakSituation([]Candidate{
		{DisplayName: "This computer — instance LEGACY", Source: "registry-port", ProbeSkip: true,
			Detail: "TCP/IP appears disabled for this instance. Enable TCP/IP for the instance in SQL Server Configuration Manager, or enter the server and its port manually in the Backup File card below."},
	})
	if e.Kind != BakUnreachable {
		t.Fatalf("kind = %q, want %q", e.Kind, BakUnreachable)
	}
	if len(e.Candidates) != 1 || !strings.Contains(e.Candidates[0].Detail, "TCP/IP appears disabled") {
		t.Errorf("disabled detail must be carried: %+v", e.Candidates)
	}
}

func TestParseManualServer(t *testing.T) {
	cases := []struct {
		in       string
		host     string
		instance string
		port     int
		wantErr  bool
	}{
		{"SRV", "SRV", "", 0, false},
		{`SRV\LEGACY`, "SRV", "LEGACY", 0, false},
		{"legacy-SERVER,1433", "legacy-SERVER", "", 1433, false},
		{"legacy-SERVER:1433", "legacy-SERVER", "", 1433, false},
		{`SRV\LEGACY,51433`, "SRV", "LEGACY", 51433, false},
		{"  127.0.0.1,1433  ", "127.0.0.1", "", 1433, false},
		{"", "", "", 0, true},
		{"   ", "", "", 0, true},
		{`\LEGACY`, "", "", 0, true},
		{`SRV\`, "", "", 0, true},
		{"HOST,0", "", "", 0, true},
		{"HOST,70000", "", "", 0, true},
		{"HOST:notaport", "HOST:notaport", "", 0, false}, // left as a host name; DNS/connect will report it
	}
	for _, c := range cases {
		host, inst, port, err := ParseManualServer(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got host=%q inst=%q port=%d", c.in, host, inst, port)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if host != c.host || inst != c.instance || port != c.port {
			t.Errorf("%q: got (%q, %q, %d), want (%q, %q, %d)", c.in, host, inst, port, c.host, c.instance, c.port)
		}
	}
}

func TestManualServerConn(t *testing.T) {
	// Windows authentication against a named instance.
	cfg, err := ManualServerConn(BakManualConn{Server: `SRV\LEGACY`, Auth: "windows"})
	if err != nil {
		t.Fatalf("windows auth: %v", err)
	}
	if !cfg.WindowsAuth || cfg.Server != "SRV" || cfg.Instance != "LEGACY" || cfg.Database != "master" {
		t.Errorf("windows auth config wrong: %+v", cfg)
	}
	// Empty auth defaults to Windows.
	cfg, err = ManualServerConn(BakManualConn{Server: "HOST,1500"})
	if err != nil || !cfg.WindowsAuth || cfg.Port != 1500 {
		t.Errorf("default auth: cfg=%+v err=%v", cfg, err)
	}
	// SQL authentication with host,port.
	cfg, err = ManualServerConn(BakManualConn{Server: "HOST,1500", Auth: "sql", User: "sa", Password: "secret"})
	if err != nil {
		t.Fatalf("sql auth: %v", err)
	}
	if cfg.WindowsAuth || cfg.User != "sa" || cfg.Password != "secret" || cfg.Port != 1500 {
		// Report fields only — never the password value.
		t.Errorf("sql auth config wrong: windowsAuth=%v user=%q port=%d passwordMatches=%v",
			cfg.WindowsAuth, cfg.User, cfg.Port, cfg.Password == "secret")
	}
	// SQL authentication without a user is rejected.
	if _, err := ManualServerConn(BakManualConn{Server: "HOST", Auth: "sql"}); err == nil {
		t.Error("sql auth without user: want error")
	}
	// Unknown authentication type is rejected.
	if _, err := ManualServerConn(BakManualConn{Server: "HOST", Auth: "kerberos"}); err == nil {
		t.Error("unknown auth: want error")
	}
	// Empty server is rejected.
	if _, err := ManualServerConn(BakManualConn{Auth: "windows"}); err == nil {
		t.Error("empty server: want error")
	}
	// An invalid database name is rejected (it is otherwise unused).
	if _, err := ManualServerConn(BakManualConn{Server: "HOST", Database: "bad;name"}); err == nil {
		t.Error("invalid database name: want error")
	}
}

func TestValidateBakPathRemote(t *testing.T) {
	// Valid forms — note these files need not exist locally: the path
	// belongs to the SQL Server computer.
	for _, p := range []string{
		`C:\Backups\legacy.bak`,
		`D:/Backups/legacy.BAK`,
		`\\FILESERVER\Share\legacy.bak`,
		"/var/backups/legacy.bak",
	} {
		if err := ValidateBakPathRemote(p); err != nil {
			t.Errorf("path %q should validate: %v", p, err)
		}
	}
	for _, p := range []string{"", "  ", "legacy.bak", `Backups\legacy.bak`, `C:\Backups\legacy.zip`, "/tmp/x.txt"} {
		if err := ValidateBakPathRemote(p); err == nil {
			t.Errorf("path %q: want error", p)
		}
	}
}
