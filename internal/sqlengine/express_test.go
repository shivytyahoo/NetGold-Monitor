package sqlengine

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// ---- the pinned download ----

func TestExpressDownloadConstant(t *testing.T) {
	if !strings.HasPrefix(ExpressDownloadURL, "https://download.microsoft.com/") ||
		!strings.HasSuffix(ExpressDownloadURL, "/SQL2025-SSEI-Expr.exe") {
		t.Errorf("Express download must stay the verified Microsoft bootstrapper URL: %s", ExpressDownloadURL)
	}
	if ExpressDownloadBytes != 4606792 {
		t.Errorf("verified bootstrapper size changed: %d", ExpressDownloadBytes)
	}
}

// ---- pure planning ----

func TestExpressPlanning(t *testing.T) {
	if got := expressServiceName("NETGOLD"); got != "MSSQL$NETGOLD" {
		t.Errorf("service name: %q", got)
	}
	if got := expressServiceName(ExpressInstanceName); got != "MSSQL$NETGOLD" {
		t.Errorf("service name for the NetGold instance: %q", got)
	}
	wantKey := `SOFTWARE\Microsoft\Microsoft SQL Server\MSSQL17.NETGOLD\MSSQLServer\SuperSocketNetLib\Tcp\IPAll`
	if got := superSocketNetLibKey("MSSQL17.NETGOLD"); got != wantKey {
		t.Errorf("SuperSocketNetLib key:\n got %q\nwant %q", got, wantKey)
	}
	plan := expressTCPPlan(ExpressPort)
	if len(plan) != 2 || plan[0] != (regStringValue{"TcpPort", "11433"}) || plan[1] != (regStringValue{"TcpDynamicPorts", ""}) {
		t.Errorf("TCP plan must pin the static port and clear dynamic ports: %+v", plan)
	}
	if got, want := expressInstanceDir(`C:\ProgramData`), filepath.Join(`C:\ProgramData`, "NetGold", "SQLEngine"); got != want {
		t.Errorf("instance dir: got %q, want %q", got, want)
	}
}

func TestExpressSSEIArgs(t *testing.T) {
	const pw = "S3cure!Passw0rd_xyz"
	args := expressSSEIArgs(ExpressInstanceName, pw, `C:\ProgramData\NetGold\SQLEngine`)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"/Q", "/ACTION=Install", "/IACCEPTSQLSERVERLICENSETERMS",
		"/INSTANCENAME=NETGOLD", "/FEATURES=SQLEngine",
		"/SECURITYMODE=SQL", "/SAPWD=" + pw,
		"/TCPENABLED=1", "/BROWSERSVCSTARTUPTYPE=Disabled",
		"/SQLSVCSTARTUPTYPE=Automatic",
		`/INSTANCEDIR=C:\ProgramData\NetGold\SQLEngine`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("bootstrapper args must contain %q, got: %s", want, joined)
		}
	}
	// The password appears exactly once (as SAPWD) — and nothing in
	// the command line asks for Windows Authentication.
	if n := strings.Count(joined, pw); n != 1 {
		t.Errorf("sa password must appear exactly once in the args, found %d", n)
	}
	if strings.Contains(strings.ToLower(joined), "trusted") {
		t.Errorf("Express setup must not involve Windows Authentication: %s", joined)
	}
}

func TestExpressExitGuidance(t *testing.T) {
	for _, code := range []uint32{0, 3010, 1641} {
		if fatal, _ := expressExitGuidance(code); fatal {
			t.Errorf("exit %d must not be fatal", code)
		}
	}
	if _, msg := expressExitGuidance(3010); !strings.Contains(msg, "restart") {
		t.Errorf("3010 must carry the restart note: %q", msg)
	}
	for _, code := range []uint32{1603, 2226324481, 0xFFFFFFFF} {
		fatal, msg := expressExitGuidance(code)
		if !fatal || msg == "" {
			t.Errorf("exit %d: want fatal with guidance", code)
		}
	}
	_, msg := expressExitGuidance(1603)
	if !strings.Contains(msg, "1603") || !strings.Contains(msg, "0x00000643") {
		t.Errorf("fatal guidance must name the code in decimal and hex: %q", msg)
	}
}

// ---- the SSPI probe classification ----

func TestClassifySSPIProbe(t *testing.T) {
	if got := classifySSPIProbe(nil); got != probeOK {
		t.Errorf("nil error: got %v, want probeOK", got)
	}
	sspiErr := fmt.Errorf("NextBytes InitializeSecurityContext failed 8009030e")
	if got := classifySSPIProbe(sspiErr); got != probeSSPIBroken {
		t.Errorf("SSPI error: got %v, want probeSSPIBroken", got)
	}
	if got := classifySSPIProbe(fmt.Errorf("dial tcp: i/o timeout")); got != probeInconclusive {
		t.Errorf("other error: got %v, want probeInconclusive", got)
	}
}

// ---- path selection ----

func TestChooseEnginePath(t *testing.T) {
	cases := []struct {
		name  string
		facts engineFacts
		want  enginePath
	}{
		{"stored sign-in works", engineFacts{StoredWorks: true, LocalDBPresent: true, Probe: probeSSPIBroken}, pathStored},
		{"localdb + probe ok", engineFacts{LocalDBPresent: true, Probe: probeOK}, pathLocalDB},
		{"localdb + sspi broken", engineFacts{LocalDBPresent: true, Probe: probeSSPIBroken}, pathExpress},
		{"localdb + probe inconclusive", engineFacts{LocalDBPresent: true, Probe: probeInconclusive}, pathExpress},
		{"localdb absent", engineFacts{Probe: probeNotRun}, pathExpress},
		{"nothing at all", engineFacts{}, pathExpress},
	}
	for _, c := range cases {
		if got := chooseEnginePath(c.facts); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFallbackAfterExpressFailure(t *testing.T) {
	if got := fallbackAfterExpressFailure(probeOK); got != fallbackLocalDB {
		t.Errorf("probe ok: got %v, want fallbackLocalDB", got)
	}
	if got := fallbackAfterExpressFailure(probeInconclusive); got != fallbackLocalDB {
		t.Errorf("probe inconclusive: got %v, want fallbackLocalDB", got)
	}
	if got := fallbackAfterExpressFailure(probeSSPIBroken); got != fallbackTerminal {
		t.Errorf("probe sspi-broken: got %v, want fallbackTerminal", got)
	}
	if got := fallbackAfterExpressFailure(probeNotRun); got != fallbackTerminal {
		t.Errorf("probe not run: got %v, want fallbackTerminal", got)
	}
}

func TestTerminalEngineError(t *testing.T) {
	msg := terminalEngineError([]string{"installing the LocalDB engine failed (boom)", "setting up the SQL Server Express engine failed (bang)"})
	for _, want := range []string{"What was tried", "LocalDB", "Express", "sign out of Windows", "password", "PIN", "8009030e"} {
		if !strings.Contains(msg, want) {
			t.Errorf("terminal message must contain %q, got:\n%s", want, msg)
		}
	}
	if msg := terminalEngineError(nil); !strings.Contains(msg, "password") {
		t.Errorf("even with no attempts listed, the remedy must be given: %q", msg)
	}
}

// ---- the Express connection config ----

func TestExpressConnConfig(t *testing.T) {
	cfg := expressConnConfig("sa", "pw123")
	if cfg.Server != "127.0.0.1" || cfg.Port != ExpressPort || cfg.User != "sa" || cfg.PipeName != "" {
		t.Errorf("express config wrong: %+v", cfg)
	}
	dsn := cfg.DSN()
	if strings.Contains(dsn, "trusted_connection") {
		t.Errorf("Express DSN must use SQL authentication, got: %s", dsn)
	}
	if cfg.UsesWindowsAuth() {
		t.Error("Express config must not count as Windows Authentication")
	}
	if !strings.Contains(cfg.TargetLabel(), "127.0.0.1,11433") {
		t.Errorf("target label should name host,port: %q", cfg.TargetLabel())
	}
}

// ---- job-level: the new engine paths ----

func TestJobExpressPathWhenSSPIBroken(t *testing.T) {
	f := &jobFakes{installed: true,
		probeErr: fmt.Errorf("NextBytes InitializeSecurityContext failed 8009030e")}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done via Express, got step=%q error=%q", st.Step, st.Error)
	}
	if !f.expressRan {
		t.Error("the Express provisioning helper must run when the SSPI probe fails")
	}
	if f.provisionRan {
		t.Error("LocalDB sign-in provisioning must NOT be attempted when the SSPI probe failed (it cannot work)")
	}
	if f.importCfg.Server != "127.0.0.1" || f.importCfg.Port != ExpressPort || f.importCfg.User != ExpressLoginUser {
		t.Errorf("import must go through the Express engine: %+v", f.importCfg)
	}
	if !strings.Contains(st.Note, "SQL Server Express") || !strings.Contains(st.Note, "stays installed") {
		t.Errorf("done note must describe the Express engine: %q", st.Note)
	}
}

func TestJobStoredExpressReused(t *testing.T) {
	f := &jobFakes{installed: false}
	swapPlatform(t, f)
	dir := t.TempDir()
	if err := SaveExpressLogin(dir, "Stored!SaPassw0rd_1"); err != nil {
		t.Fatalf("save express login: %v", err)
	}
	s := &Setup{DataDir: dir}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done via the stored Express sign-in, got step=%q error=%q", st.Step, st.Error)
	}
	if f.expressRan || f.installRan || f.provisionRan {
		t.Error("a working stored sign-in must skip all setup (no install, no provisioning)")
	}
	if f.importCfg.User != ExpressLoginUser || f.importCfg.Password != "Stored!SaPassw0rd_1" {
		t.Errorf("import must use the stored sa sign-in: user=%q", f.importCfg.User)
	}
}

func TestJobTerminalGuidance(t *testing.T) {
	f := &jobFakes{installed: true,
		probeErr:   fmt.Errorf("NextBytes InitializeSecurityContext failed 8009030e"),
		expressErr: fmt.Errorf("the SQL Server Express installer exited with code 1603 (0x00000643)")}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepFailed {
		t.Fatalf("want failed, got step=%q", st.Step)
	}
	for _, want := range []string{"What was tried", "LocalDB", "Express", "sign out of Windows", "password", "PIN"} {
		if !strings.Contains(st.Error, want) {
			t.Errorf("terminal error must contain %q, got:\n%s", want, st.Error)
		}
	}
}

func TestJobLocalDBFailureFallsBackToExpress(t *testing.T) {
	// LocalDB install fails (1603) and no probe could run: the
	// Express path is tried next and completes the job.
	f := &jobFakes{installed: false, installCode: 1603}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done via Express fallback, got step=%q error=%q", st.Step, st.Error)
	}
	if !f.expressRan || f.importCfg.User != ExpressLoginUser {
		t.Errorf("import must complete through Express: ran=%v cfg=%+v", f.expressRan, f.importCfg)
	}
}
