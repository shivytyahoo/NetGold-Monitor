package sqlengine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netgold/internal/legacyimport"
	"netgold/internal/store"
)

// ---- pure helpers ----

const localDBInfoSample = `Name:                   NetGold
Version:                16.0.1000.6
Shared name:
Owner:                  TESTPC\TestUser
Auto-create:            No
State:                  Running
Last start time:        10/9/2026 11:00:00 AM
Instance pipe name:     np:\\.\pipe\LOCALDB#1A2B3C4D\tsql\query
`

func TestParseLocalDBInfo(t *testing.T) {
	info := ParseLocalDBInfo(localDBInfoSample)
	if info.Name != "NetGold" || info.Version != "16.0.1000.6" || info.State != "Running" {
		t.Errorf("info parsed wrong: %+v", info)
	}
	if info.PipeName != `np:\\.\pipe\LOCALDB#1A2B3C4D\tsql\query` {
		t.Errorf("pipe name wrong: %q", info.PipeName)
	}
	if got := PipePathFromInfo(info.PipeName); got != `\\.\pipe\LOCALDB#1A2B3C4D\tsql\query` {
		t.Errorf("pipe path: %q", got)
	}
	// Stopped instance: no pipe name line is printed.
	stopped := ParseLocalDBInfo("Name:  NetGold\nState:  Stopped\n")
	if stopped.State != "Stopped" || stopped.PipeName != "" {
		t.Errorf("stopped info: %+v", stopped)
	}
	if got := ParseLocalDBInfo(""); got != (LocalDBInfo{}) {
		t.Errorf("empty output: %+v", got)
	}
	if got := PipePathFromInfo(`\\.\pipe\X`); got != `\\.\pipe\X` {
		t.Errorf("pipe without np: prefix must pass through: %q", got)
	}
}

func TestVerifySignatureResult(t *testing.T) {
	status, signer := ParseAuthenticodeOutput("STATUS=Valid\nSIGNER=CN=Microsoft Corporation, O=Microsoft Corporation, L=Redmond, S=Washington, C=US\n")
	if status != "Valid" || !strings.Contains(signer, "Microsoft Corporation") {
		t.Fatalf("parse: status=%q signer=%q", status, signer)
	}
	if err := VerifySignatureResult(status, signer); err != nil {
		t.Errorf("valid Microsoft signature must pass: %v", err)
	}
	if err := VerifySignatureResult("HashMismatch", signer); err == nil {
		t.Error("invalid signature status must fail")
	}
	if err := VerifySignatureResult("Valid", "CN=Some Random Publisher"); err == nil {
		t.Error("non-Microsoft signer must fail")
	}
	if err := VerifySignatureResult("", ""); err == nil {
		t.Error("empty facts must fail")
	}
}

func TestMSIExitGuidance(t *testing.T) {
	for _, code := range []int{0, 3010, 1638} {
		if fatal, _ := msiExitGuidance(code); fatal {
			t.Errorf("exit %d must not be fatal", code)
		}
	}
	for _, code := range []int{1603, 1618, 1625, 9999} {
		fatal, msg := msiExitGuidance(code)
		if !fatal || msg == "" {
			t.Errorf("exit %d: want fatal with guidance, got fatal=%v msg=%q", code, fatal, msg)
		}
	}
	if _, msg := msiExitGuidance(1603); !strings.Contains(msg, "1603") {
		t.Errorf("1603 guidance must name the code: %s", msg)
	}
}

func TestDownloadPercent(t *testing.T) {
	if downloadPercent(0, 100) != 0 || downloadPercent(50, 100) != 50 ||
		downloadPercent(100, 100) != 100 || downloadPercent(250, 100) != 100 ||
		downloadPercent(10, 0) != 0 || downloadPercent(10, -5) != 0 {
		t.Error("percent calculation wrong")
	}
}

func TestDownloadURLConstant(t *testing.T) {
	if !strings.HasPrefix(LocalDBDownloadURL, "https://download.microsoft.com/") ||
		!strings.HasSuffix(LocalDBDownloadURL, "/SqlLocalDB.msi") {
		t.Errorf("download URL must stay the verified Microsoft SqlLocalDB.msi URL: %s", LocalDBDownloadURL)
	}
	if LocalDBDownloadBytes != 63508480 {
		t.Errorf("verified size changed: %d", LocalDBDownloadBytes)
	}
}

func TestStepLabels(t *testing.T) {
	for _, s := range StepOrder {
		if StepLabel(s) == s {
			t.Errorf("step %q has no human label", s)
		}
	}
	if StepLabel(StepDone) == StepDone || StepLabel(StepFailed) == StepFailed {
		t.Error("terminal steps need labels too")
	}
}

func TestDownloadFile(t *testing.T) {
	payload := strings.Repeat("NetGold", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		fmt.Fprint(w, payload)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f.bin")
	var lastPct int
	if err := downloadFile(context.Background(), srv.URL, dest, func(p int) { lastPct = p }); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != payload {
		t.Errorf("downloaded content wrong (err=%v, %d bytes)", err, len(got))
	}
	if lastPct != 100 {
		t.Errorf("final progress = %d, want 100", lastPct)
	}
	// A 404 must be an error, not an empty file treated as success.
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	if err := downloadFile(context.Background(), srv404.URL, filepath.Join(t.TempDir(), "x.bin"), nil); err == nil {
		t.Error("404 download: want error")
	}
}

// ---- job state machine (platform ops faked) ----

type jobFakes struct {
	installed   bool
	verifyErr   error
	installCode int
	installRan  bool
	verifyRan   bool
	ensureErr   error
	importErr   error
	warnings    []string
	installLog  string // written by the fake installer to the log path it is given

	// engine-login provisioning (v3.0.1)
	provisionRan      bool
	provisionedUser   string
	provisionedPW     string
	provisionErr      error
	provisionChildRan bool
	provisionChildErr error
	pingErr           error
	importCfg         legacyimport.ConnConfig

	// express engine path (v3.0.2)
	probeErr         error // SSPI probe against LocalDB
	expressErr       error // provisioning helper failure
	expressDlErr     error // bootstrapper download failure
	expressRan       bool
	expressInstalled bool
}

func swapPlatform(t *testing.T, f *jobFakes) {
	t.Helper()
	oldSupported, oldFind, oldVerify, oldInstall, oldEnsure, oldState, oldRemove, oldImport, oldDownload, oldPreflight :=
		platSupported, platFindLocalDB, platVerifySignature, platInstallMSI, platEnsureInstance, platInstanceState, platRemoveInstance, importBakWithProgress, downloadMSI, platPreflightWarnings
	oldProbe, oldProvisionExpress, oldFindExpress, oldDownloadExpress :=
		platProbeSSPI, platProvisionExpress, platFindExpress, downloadExpress
	platSupported = func() bool { return true }
	platPreflightWarnings = func() []string { return f.warnings }
	downloadMSI = func(ctx context.Context, dest string, progress func(int)) error {
		if progress != nil {
			progress(100)
		}
		return os.WriteFile(dest, []byte("fake msi"), 0o600)
	}
	platFindLocalDB = func() (string, bool) { return `C:\fake\SqlLocalDB.exe`, f.installed }
	platVerifySignature = func(string) error { f.verifyRan = true; return f.verifyErr }
	platInstallMSI = func(msiPath, logPath string) (int, error) {
		f.installRan = true
		// A successful install (0/3010/1638) makes LocalDB findable;
		// a fatal one (1603, ...) does not.
		if f.installCode == 0 || f.installCode == 3010 || f.installCode == 1638 {
			f.installed = true
		}
		if f.installLog != "" && logPath != "" {
			if err := os.WriteFile(logPath, []byte(f.installLog), 0o600); err != nil {
				return -1, err
			}
		}
		return f.installCode, nil
	}
	platProbeSSPI = func(string) error { return f.probeErr }
	platFindExpress = func() bool { return f.expressInstalled }
	downloadExpress = func(ctx context.Context, dest string, progress func(int)) error {
		if f.expressDlErr != nil {
			return f.expressDlErr
		}
		if progress != nil {
			progress(100)
		}
		fh, err := os.Create(dest)
		if err != nil {
			return err
		}
		defer fh.Close()
		// runExpressImport checks the downloaded size exactly.
		return fh.Truncate(ExpressDownloadBytes)
	}
	platProvisionExpress = func(dataDir, installerPath string, progress func(string)) error {
		f.expressRan = true
		if f.expressErr != nil {
			return f.expressErr
		}
		f.expressInstalled = true
		pw, err := GenerateEnginePassword()
		if err != nil {
			return err
		}
		return SaveExpressLogin(dataDir, pw)
	}
	platEnsureInstance = func(string) (string, error) {
		if f.ensureErr != nil {
			return "", f.ensureErr
		}
		return `np:\\.\pipe\LOCALDB#ABCDEF12\tsql\query`, nil
	}
	// Engine-login provisioning fakes: "DPAPI" is a prefix scheme,
	// provisioning records its credentials, the child simulation
	// provisions + saves like the real child, and pings succeed.
	oldProtect, oldUnprotect, oldProvisionHere, oldProvisionChild, oldPing, oldTighten :=
		platProtectSecret, platUnprotectSecret, platProvisionLoginHere, platRunProvisionChild, platPingConn, platTightenLoginFile
	platProtectSecret = func(b []byte) ([]byte, error) {
		return append([]byte("FAKEPROT:"), b...), nil
	}
	platUnprotectSecret = func(b []byte) ([]byte, error) {
		s := string(b)
		if !strings.HasPrefix(s, "FAKEPROT:") {
			return nil, fmt.Errorf("not a fake-protected blob")
		}
		return []byte(strings.TrimPrefix(s, "FAKEPROT:")), nil
	}
	platProvisionLoginHere = func(pipePath, user, password string) error {
		f.provisionRan = true
		f.provisionedUser, f.provisionedPW = user, password
		return f.provisionErr
	}
	platRunProvisionChild = func(dataDir string, progress func(string)) error {
		f.provisionChildRan = true
		if f.provisionChildErr != nil {
			return f.provisionChildErr
		}
		pw, err := GenerateEnginePassword()
		if err != nil {
			return err
		}
		f.provisionedUser, f.provisionedPW = EngineLoginUser, pw
		return SaveEngineLogin(dataDir, EngineLoginUser, pw)
	}
	platPingConn = func(cfg legacyimport.ConnConfig) error { return f.pingErr }
	platTightenLoginFile = func(string) {}
	importBakWithProgress = func(cfg legacyimport.ConnConfig, path string, st *store.Store, onPhase func(string)) (*legacyimport.Report, error) {
		f.importCfg = cfg
		if cfg.PipeName != "" {
			// LocalDB path: the pipe from the fake instance.
			if cfg.PipeName != `\\.\pipe\LOCALDB#ABCDEF12\tsql\query` {
				return nil, fmt.Errorf("job passed the wrong pipe name to the importer")
			}
		} else {
			// Express path: loopback TCP, static port, 'sa' login.
			if cfg.Server != "127.0.0.1" || cfg.Port != ExpressPort || cfg.User != ExpressLoginUser {
				return nil, fmt.Errorf("job passed a wrong Express config to the importer: %+v", cfg)
			}
		}
		if onPhase != nil {
			onPhase("restoring")
			onPhase("importing")
		}
		if f.importErr != nil {
			return nil, f.importErr
		}
		return &legacyimport.Report{}, nil
	}
	t.Cleanup(func() {
		platSupported, platFindLocalDB, platVerifySignature, platInstallMSI, platEnsureInstance, platInstanceState, platRemoveInstance, importBakWithProgress, downloadMSI, platPreflightWarnings =
			oldSupported, oldFind, oldVerify, oldInstall, oldEnsure, oldState, oldRemove, oldImport, oldDownload, oldPreflight
		platProtectSecret, platUnprotectSecret, platProvisionLoginHere, platRunProvisionChild, platPingConn, platTightenLoginFile =
			oldProtect, oldUnprotect, oldProvisionHere, oldProvisionChild, oldPing, oldTighten
		platProbeSSPI, platProvisionExpress, platFindExpress, downloadExpress =
			oldProbe, oldProvisionExpress, oldFindExpress, oldDownloadExpress
	})
}

func fakeBakFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "legacy.bak")
	if err := os.WriteFile(p, []byte("fake backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitJob(t *testing.T, s *Setup) JobStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := s.Status()
		if !st.Running {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish in time")
	return JobStatus{}
}

func TestJobSuccessFromScratch(t *testing.T) {
	f := &jobFakes{installed: false}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done, got step=%q error=%q (failed at %q)", st.Step, st.Error, st.FailedStep)
	}
	if !f.verifyRan || !f.installRan {
		t.Error("fresh machine: download verify + install must both run")
	}
	if st.Report == nil || !st.CanRemoveInstance {
		t.Errorf("done status incomplete: %+v", st)
	}
	if !strings.Contains(st.Note, "stays installed") {
		t.Errorf("done note must explain the engine stays installed: %q", st.Note)
	}
}

func TestJobSkipsSetupWhenInstalled(t *testing.T) {
	f := &jobFakes{installed: true}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("want done, got %q (%q)", st.Step, st.Error)
	}
	if f.verifyRan || f.installRan {
		t.Error("already installed: download/verify/install must be skipped (no network use)")
	}
}

func TestJobSignatureFailureAbortsBeforeInstall(t *testing.T) {
	f := &jobFakes{installed: false, verifyErr: fmt.Errorf("the downloaded installer's signature is not valid (status: HashMismatch) — the file was NOT run")}
	swapPlatform(t, f)
	s := &Setup{}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepFailed || st.FailedStep != StepVerifying {
		t.Fatalf("want failure at verifying, got step=%q failed=%q", st.Step, st.FailedStep)
	}
	if f.installRan {
		t.Error("an unverified installer must NEVER be run")
	}
	if !strings.Contains(st.Error, "Verifying") {
		t.Errorf("error must be stage-labelled: %q", st.Error)
	}
}

func TestJobInstallFailure1603(t *testing.T) {
	// v3.0.2: a 1603 LocalDB failure first falls back to the
	// Express engine; the job only fails when that fails too, and
	// the terminal message names both attempts plus the remedy.
	f := &jobFakes{installed: false, installCode: 1603,
		expressErr: fmt.Errorf("the SQL Server Express installer exited with code 1603 (0x00000643)")}
	swapPlatform(t, f)
	s := &Setup{}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepFailed || st.FailedStep != StepInstalling {
		t.Fatalf("want failure at installing, got step=%q failed=%q", st.Step, st.FailedStep)
	}
	if !strings.Contains(st.Error, "1603") {
		t.Errorf("error must carry the 1603 guidance: %q", st.Error)
	}
	if !strings.Contains(st.Error, "What was tried") || !strings.Contains(st.Error, "password") {
		t.Errorf("terminal error must name what was tried and give the remedy: %q", st.Error)
	}
}

func TestJobImportFailureStep(t *testing.T) {
	f := &jobFakes{installed: true, importErr: fmt.Errorf("restoring the backup: simulated restore failure")}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepFailed || st.FailedStep != StepImporting {
		// The fake fires both phases before erroring, so the job's last
		// phase is "importing".
		t.Fatalf("want failure labelled at the importer's last phase, got step=%q failed=%q", st.Step, st.FailedStep)
	}
}

func TestStartRefusals(t *testing.T) {
	// Unsupported platform.
	old := platSupported
	platSupported = func() bool { return false }
	s := &Setup{}
	if err := s.Start(fakeBakFile(t), false); err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Errorf("unsupported platform: want a Windows message, got %v", err)
	}
	platSupported = old

	// Running as the Windows service.
	f := &jobFakes{installed: true}
	swapPlatform(t, f)
	s2 := &Setup{}
	if err := s2.Start(fakeBakFile(t), true); err == nil || !strings.Contains(err.Error(), "service") {
		t.Errorf("service run: want the desktop-program guidance, got %v", err)
	}
	// Missing backup file.
	s3 := &Setup{}
	if err := s3.Start(filepath.Join(t.TempDir(), "nope.bak"), false); err == nil {
		t.Error("missing backup file: want a validation error")
	}
}

func TestFreshStatusIsIdle(t *testing.T) {
	s := &Setup{}
	if st := s.Status(); st.Step != StepIdle || st.Running {
		t.Errorf("fresh setup status: %+v", st)
	}
}

// ---- installer log evidence + pre-flight helpers ----

// A synthetic verbose MSI log of a failed install: an Error 1925
// early on, then the failing action's context lines ending in
// "Return value 3", with one unrelated line that must be left out.
const msiLogFixture = `=== Logging started: 10/9/2026  13:20:01 ===
MSI (s) (A4:B0) [13:20:01:001]: Grabbed execution mutex.
MSI (s) (A4:B0) [13:20:02:002]: PROPERTY CHANGE: adding a property nobody needs UNRELATED-LINE
MSI (s) (A4:B0) [13:20:03:003]: Error 1925. You do not have sufficient privileges to complete this installation for all users of the machine.
MSI (s) (A4:B0) [13:20:04:004]: Doing action: InstallFinalize
Action 13:20:04: InstallFinalize. Installing the product.
MSI (s) (A4:B0) [13:20:05:005]: Note: 1: 1722 2: C:\Config.Msi\ab12.rbf
Error 1722. There is a problem with this Windows Installer package.
Action ended 13:20:05: InstallFinalize. Return value 3.
MSI (s) (A4:B0) [13:20:06:006]: Windows Installer installed the product. Installation failed.
=== Logging stopped: 10/9/2026  13:20:06 ===
`

func TestExtractInstallLogEvidence(t *testing.T) {
	got := ExtractInstallLogEvidence(msiLogFixture)
	joined := strings.Join(got, "\n")
	for _, want := range []string{"Error 1925", "Error 1722", "Note: 1: 1722", "Return value 3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("evidence must contain %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "UNRELATED-LINE") {
		t.Errorf("unrelated lines must not be extracted:\n%s", joined)
	}
	if len(got) > 15 {
		t.Errorf("evidence capped at 15 lines, got %d", len(got))
	}
	// Log order is preserved: the early Error 1925 comes first.
	if !strings.HasPrefix(got[0], "MSI (s) (A4:B0) [13:20:03") {
		t.Errorf("first evidence line should be the Error 1925 line, got %q", got[0])
	}
}

func TestExtractInstallLogEvidenceCapAndCleanLog(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "MSI (s) [13:20:00]: Error 1706. No valid source could be found, attempt %d.\n", i)
	}
	if got := ExtractInstallLogEvidence(b.String()); len(got) != 15 {
		t.Errorf("40 error lines must be capped at 15, got %d", len(got))
	}
	clean := "=== Logging started ===\nAction ended 13:20:05: Install. Return value 1.\n=== Logging stopped ===\n"
	if got := ExtractInstallLogEvidence(clean); len(got) != 0 {
		t.Errorf("a successful log has no failure evidence, got %v", got)
	}
	if got := ExtractInstallLogEvidence(""); len(got) != 0 {
		t.Errorf("empty log: got %v", got)
	}
}

func utf16LEBytes(s string, bom bool) []byte {
	var out []byte
	if bom {
		out = append(out, 0xFF, 0xFE)
	}
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func TestDecodeMSILog(t *testing.T) {
	text := "MSI (s) [13:20:05]: Return value 3.\r\nDone."
	if got := DecodeMSILog(utf16LEBytes(text, true)); got != text {
		t.Errorf("UTF-16LE with BOM decoded wrong: %q", got)
	}
	if got := DecodeMSILog(utf16LEBytes(text, false)); got != text {
		t.Errorf("UTF-16LE without BOM decoded wrong: %q", got)
	}
	plain := "plain ASCII log line, nothing special here\n"
	if got := DecodeMSILog([]byte(plain)); got != plain {
		t.Errorf("plain text must pass through unchanged: %q", got)
	}
	// End to end: the fixture as UTF-16LE bytes yields the same evidence.
	ev := ExtractInstallLogEvidence(DecodeMSILog(utf16LEBytes(msiLogFixture, true)))
	if !strings.Contains(strings.Join(ev, "\n"), "Error 1925") {
		t.Errorf("evidence from a UTF-16LE log must be found, got %v", ev)
	}
}

func TestDiskSpaceWarning(t *testing.T) {
	if w := DiskSpaceWarning(4 << 30); w != "" {
		t.Errorf("4 GiB free must not warn: %q", w)
	}
	if w := DiskSpaceWarning(minFreeDiskBytes); w != "" {
		t.Errorf("exactly at the threshold must not warn: %q", w)
	}
	w := DiskSpaceWarning(1 << 30)
	if !strings.Contains(w, "1.0 GB") || !strings.Contains(w, "free space") {
		t.Errorf("1 GiB free must warn with the amount: %q", w)
	}
	if w := DiskSpaceWarning(0); w == "" {
		t.Error("0 free must warn")
	}
}

func TestJobInstallFailureIncludesLogDetails(t *testing.T) {
	f := &jobFakes{installed: false, installCode: 1603, installLog: msiLogFixture,
		expressErr: fmt.Errorf("the SQL Server Express installer exited with code 1603 (0x00000643)")}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepFailed || st.FailedStep != StepInstalling {
		t.Fatalf("want failure at installing, got step=%q failed=%q", st.Step, st.FailedStep)
	}
	for _, want := range []string{"1603", "Installer log details", "Error 1925", "Return value 3", "sqlengine-install.log"} {
		if !strings.Contains(st.Error, want) {
			t.Errorf("failure message must contain %q, got:\n%s", want, st.Error)
		}
	}
	// The log file itself stays in the data dir for later inspection.
	if _, err := os.Stat(filepath.Join(s.DataDir, "sqlengine-install.log")); err != nil {
		t.Errorf("install log must be kept in the data dir: %v", err)
	}
}

func TestJobPreflightWarningsSurface(t *testing.T) {
	f := &jobFakes{installed: true, warnings: []string{"warn-one", "warn-two"}}
	swapPlatform(t, f)
	s := &Setup{DataDir: t.TempDir()}
	if err := s.Start(fakeBakFile(t), false); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitJob(t, s)
	if st.Step != StepDone {
		t.Fatalf("warnings must not block the job, got step=%q error=%q", st.Step, st.Error)
	}
	if len(st.Warnings) != 2 || st.Warnings[0] != "warn-one" || st.Warnings[1] != "warn-two" {
		t.Errorf("status must carry the pre-flight warnings: %+v", st.Warnings)
	}
}
