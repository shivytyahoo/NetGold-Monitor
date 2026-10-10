// Package sqlengine implements NetGold's automatic SQL engine setup
// for backup (.bak) imports (v2.2).
//
// A .bak file can only be opened by a SQL Server engine. When a
// Windows machine has no usable SQL Server at all, NetGold sets one
// up itself. Every installer it runs is downloaded from Microsoft
// and Authenticode-verified first (publisher must be Microsoft,
// status must be Valid — anything else aborts before the file is
// ever run), and every install runs elevated, via the real Windows
// UAC approval prompt (a plain unelevated /quiet child process gets
// no prompt and fails).
//
// Two engines, chosen by what the machine can actually use (the
// selection is pure logic — see express.go):
//
//   - LocalDB (since v2.2): Microsoft's free SQL Server Express
//     LocalDB, a dedicated instance reached over its named pipe.
//     Since v3.0.1 the setup provisions a dedicated SQL login on
//     the instance (see enginelogin.go), so imports sign in with
//     that login instead of Windows Authentication. BUT creating
//     that login needs a first sysadmin connection, and on LocalDB
//     that connection is the owner's SSPI sign-in — so the LocalDB
//     path is only chosen when a trial SSPI connection succeeds.
//   - SQL Server Express as a service (since v3.0.2): a named
//     instance (NETGOLD) installed with mixed-mode security and a
//     generated 'sa' password, so a working SQL sign-in exists the
//     moment setup finishes — no SSPI anywhere. This is the path
//     for sessions whose Windows Authentication has no usable
//     credentials (error 8009030e — PIN/Windows Hello sign-ins,
//     installer-launched and service sessions), where LocalDB
//     provisioning can never get started.
//
// The platform-specific operations live in engine_windows.go,
// express_windows.go and repair_windows.go; other platforms report
// the setup as unavailable.
package sqlengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"netgold/internal/legacyimport"
	"netgold/internal/store"
)

// LocalDBDownloadURL is Microsoft's standalone SQL Server 2022
// LocalDB installer (SqlLocalDB.msi) on download.microsoft.com.
// Verified 2026-10-09: HTTP 200, Content-Length 63,508,480 bytes, OLE2
// (MSI) content.
const LocalDBDownloadURL = "https://download.microsoft.com/download/3/8/d/38de7036-2433-4207-8eae-06e247e17b25/SqlLocalDB.msi"

// LocalDBDownloadBytes is the verified size of the installer above.
const LocalDBDownloadBytes = 63508480

// LocalDBInstanceName is the dedicated LocalDB instance NetGold
// creates for backup imports (kept separate from any developer
// instances the user may have).
const LocalDBInstanceName = "NetGold"

// Engine-kept note shown after a successful setup import.
const engineKeptNote = "The LocalDB engine (instance \"" + LocalDBInstanceName + "\") stays installed so future backup imports work right away. " +
	"It uses no resources while stopped. You can remove the instance with the Remove Engine button; the LocalDB program itself can be removed later via Windows Settings > Apps."

// ---- platform hooks (defaults: unsupported; Windows replaces them) ----

var (
	platSupported = func() bool { return false }
	// findLocalDB returns the SqlLocalDB.exe path and whether the
	// LocalDB binaries are installed at all.
	platFindLocalDB = func() (string, bool) { return "", false }
	// verifySignature checks an installer's Authenticode signature.
	platVerifySignature = func(path string) error { return errUnsupported() }
	// installMSI runs the installer quietly, writing a verbose log to
	// logPath, and returns its exit code. On Windows the install runs
	// ELEVATED (see engine_windows.go): when NetGold itself is not
	// running as administrator, the user gets a real UAC approval
	// prompt — a plain unelevated child msiexec with /quiet gets no
	// prompt at all and simply fails (the v2.2.3 exit-code-1603 bug).
	platInstallMSI = func(msiPath, logPath string) (int, error) { return -1, errUnsupported() }
	// preflightWarnings reports conditions that make an install
	// likely to fail (a pending Windows restart, very low disk
	// space). Warnings inform the user; they never block the attempt.
	platPreflightWarnings = func() []string { return nil }
	// bundledMSIPath reports where an installer-bundled SqlLocalDB.msi
	// would live (engine folder next to the program). A variable so
	// tests can point it at a fixture.
	platBundledMSIPath = func() string { return bundledMSIPath() }
	// ensureInstance creates (if needed) and starts the NetGold
	// instance and returns its pipe name.
	platEnsureInstance = func(exePath string) (string, error) { return "", errUnsupported() }
	// instanceState returns "Running", "Stopped" or "" (no instance).
	platInstanceState = func(exePath string) string { return "" }
	// removeInstance stops and deletes the NetGold instance.
	platRemoveInstance = func(exePath string) error { return errUnsupported() }
)

func errUnsupported() error {
	return fmt.Errorf("automatic engine setup is available on Windows only")
}

// installCancelledMessage is the exact user-facing text for "the user
// pressed No (or closed) the Windows administrator (UAC) approval
// prompt". The Windows installer path returns an error carrying this
// text; it is a constant so tests can assert on it.
const installCancelledMessage = "Setup was cancelled at the Windows approval prompt. " +
	"The SQL engine cannot be installed without approval — run setup again and choose Yes."

// importBakWithProgress is the backup importer the job finishes with
// (a variable so the job's state machine is unit-testable without a
// SQL Server; production always uses the real importer).
var importBakWithProgress = legacyimport.ImportBakWithProgress

// downloadMSI fetches the LocalDB installer (a variable for the same
// testability reason; production downloads from Microsoft).
var downloadMSI = func(ctx context.Context, dest string, progress func(int)) error {
	return downloadFile(ctx, LocalDBDownloadURL, dest, progress)
}

// Supported reports whether automatic engine setup can run on this
// platform/build at all (Windows only).
func Supported() bool { return platSupported() }

// ---- engine state (GET /api/legacy/bak-engine) ----

// State describes what the backup-import engine situation is right
// now. RunningAsService is filled in by the web layer (it owns that
// fact); everything else is computed here.
type State struct {
	Platform             string `json:"platform"`
	SetupSupported       bool   `json:"setup_supported"`
	RunningAsService     bool   `json:"running_as_service"`
	UsableSQL            bool   `json:"usable_sql"`
	LocalDBInstalled     bool   `json:"localdb_installed"`
	LocalDBInstanceState string `json:"localdb_instance_state"` // "Running", "Stopped" or ""
	ExpressInstalled     bool   `json:"express_installed"`      // the NETGOLD Express instance's service exists
	BundledInstaller     bool   `json:"bundled_installer"`      // an engine\SqlLocalDB.msi sits next to the program
	DownloadURL          string `json:"download_url"`
	DownloadBytes        int64  `json:"download_bytes"`
}

// CurrentState computes the engine state, including whether any
// detected SQL Server already accepts a connection.
func CurrentState() State {
	st := State{
		Platform:         runtime.GOOS,
		SetupSupported:   platSupported(),
		DownloadURL:      LocalDBDownloadURL,
		DownloadBytes:    LocalDBDownloadBytes,
		LocalDBInstalled: false,
		ExpressInstalled: platFindExpress(),
	}
	if exe, ok := platFindLocalDB(); ok {
		st.LocalDBInstalled = true
		st.LocalDBInstanceState = platInstanceState(exe)
	}
	st.BundledInstaller = fileExistsNonEmpty(platBundledMSIPath())
	for _, c := range legacyimport.Detect() {
		if c.CanConnect {
			st.UsableSQL = true
			break
		}
	}
	return st
}

// RemoveInstance stops and deletes the dedicated LocalDB instance.
// The LocalDB program (MSI) itself is deliberately NOT uninstalled —
// that stays a normal Windows Settings action.
func RemoveInstance() (string, error) {
	if !platSupported() {
		return "", errUnsupported()
	}
	exe, ok := platFindLocalDB()
	if !ok {
		return "", fmt.Errorf("LocalDB is not installed on this computer")
	}
	if err := platRemoveInstance(exe); err != nil {
		return "", err
	}
	return "The LocalDB instance \"" + LocalDBInstanceName + "\" was stopped and removed. " +
		"The LocalDB program itself is still installed; you can remove it via Windows Settings > Apps if you want to.", nil
}

// ---- pure helpers (unit-tested) ----

// downloadPercent converts byte counts into a 0–100 percentage. A
// non-positive total (unknown length) reports 0.
func downloadPercent(done, total int64) int {
	if total <= 0 || done <= 0 {
		return 0
	}
	p := int(done * 100 / total)
	if p > 100 {
		p = 100
	}
	return p
}

// LocalDBInfo is the parsed result of `SqlLocalDB info <instance>`.
type LocalDBInfo struct {
	Name     string
	Version  string
	State    string // "Running", "Stopped", ...
	PipeName string
}

// ParseLocalDBInfo parses SqlLocalDB.exe info output. Pure function.
func ParseLocalDBInfo(output string) LocalDBInfo {
	var info LocalDBInfo
	for _, line := range strings.Split(output, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "name":
			info.Name = v
		case "version":
			info.Version = v
		case "state":
			info.State = v
		case "instance pipe name":
			// The value itself starts with "np:" — keep the full
			// value; PipePathFromInfo strips it when connecting.
			info.PipeName = v
		}
	}
	return info
}

// PipePathFromInfo turns an info pipe value ("np:\\.\pipe\...") into
// the raw pipe path ("\\.\pipe\...") that ConnConfig.PipeName expects.
func PipePathFromInfo(infoPipe string) string {
	p := strings.TrimSpace(infoPipe)
	p = strings.TrimPrefix(p, "np:")
	return p
}

// ParseAuthenticodeOutput extracts the two facts NetGold requires from
// its PowerShell signature check: the signature status and the signer
// subject. Pure function; the caller enforces "Valid" + Microsoft.
func ParseAuthenticodeOutput(output string) (status, signer string) {
	for _, line := range strings.Split(output, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "STATUS":
			status = strings.TrimSpace(v)
		case "SIGNER":
			signer = strings.TrimSpace(v)
		}
	}
	return status, signer
}

// VerifySignatureResult enforces the signature policy on parsed
// output: status must be Valid and the signer must be Microsoft.
// Pure function — the Windows side only collects the raw facts.
func VerifySignatureResult(status, signer string) error {
	if !strings.EqualFold(status, "Valid") {
		return fmt.Errorf("the downloaded installer's signature is not valid (status: %s) — the file was NOT run", status)
	}
	if !strings.Contains(signer, "Microsoft") {
		return fmt.Errorf("the downloaded installer was not signed by Microsoft (signer: %s) — the file was NOT run", signer)
	}
	return nil
}

// msiExitGuidance maps an msiexec exit code to (fatal, message).
// Pure function.
func msiExitGuidance(code int) (fatal bool, message string) {
	switch code {
	case 0:
		return false, ""
	case 3010:
		return false, "The installer asks for a Windows restart to finish (exit code 3010); the engine is installed and usable now, and a restart is recommended when convenient."
	case 1638:
		return false, "Another version of LocalDB is already installed (exit code 1638) — continuing with the installed version."
	case 1603:
		return true, "The LocalDB installer failed (exit code 1603, a fatal installation error). Common causes: a pending Windows restart, an interrupted earlier SQL Server setup, or not enough disk space. Restart Windows, then try again."
	case 1618:
		return true, "Another installation is already in progress (exit code 1618). Wait for it to finish (check for a running Windows Update or installer), then try again."
	case 1625:
		return true, "The installation was blocked by a system policy (exit code 1625). Ask your IT administrator to allow the Microsoft SqlLocalDB.msi installer."
	default:
		return true, fmt.Sprintf("The LocalDB installer exited with code %d. Check that you approved the Windows administrator prompt (UAC) and that no other installer is running, then try again.", code)
	}
}

// ---- installer log evidence (pure helpers, unit-tested) ----

// DecodeMSILog turns raw msiexec log bytes into text. Windows
// Installer writes verbose logs as UTF-16LE (usually with a BOM);
// anything else is passed through as-is. Pure function.
func DecodeMSILog(data []byte) string {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return decodeUTF16LE(data[2:])
	}
	// No BOM: still UTF-16LE when almost every odd byte is zero
	// (the log text is overwhelmingly ASCII).
	if len(data) >= 4 {
		n := len(data) / 2
		if n > 512 {
			n = 512
		}
		zero := 0
		for i := 0; i < n; i++ {
			if data[2*i+1] == 0 {
				zero++
			}
		}
		if zero > n*9/10 {
			return decodeUTF16LE(data)
		}
	}
	return string(data)
}

func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

var msiErrorLine = regexp.MustCompile(`Error 1[79][0-9][0-9]\b`)

// ExtractInstallLogEvidence pulls the lines that explain an MSI
// failure out of a verbose installer log: the context lines around
// every "Return value 3" (the point where an install action failed)
// plus any "Error 17xx" / "Error 19xx" lines, in log order, capped
// at 15 lines. Pure function.
func ExtractInstallLogEvidence(logText string) []string {
	const maxLines = 15
	lines := strings.Split(logText, "\n")
	keep := make([]bool, len(lines))
	for i, ln := range lines {
		if msiErrorLine.MatchString(ln) {
			keep[i] = true
		}
		if strings.Contains(ln, "Return value 3") {
			for j := i - 4; j <= i; j++ {
				if j >= 0 {
					keep[j] = true
				}
			}
		}
	}
	var out []string
	for i, ln := range lines {
		if !keep[i] {
			continue
		}
		ln = strings.TrimRight(ln, " \t\r")
		if strings.TrimSpace(ln) == "" {
			continue
		}
		out = append(out, ln)
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

// installLogEvidence reads an installer log file and returns the
// formatted "Installer log details" block for a failure message, or
// "" when the log is missing or holds no failure evidence.
func installLogEvidence(logPath string) string {
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := ExtractInstallLogEvidence(DecodeMSILog(data))
	if len(lines) == 0 {
		return ""
	}
	return "Installer log details (from " + logPath + "):\n" + strings.Join(lines, "\n")
}

// minFreeDiskBytes is the free system-drive space below which the
// pre-flight check warns: the LocalDB install plus Windows
// Installer's own bookkeeping need roughly this much room.
const minFreeDiskBytes = 3 * (1 << 29) // 1.5 GiB

// DiskSpaceWarning returns a pre-flight warning when the free space
// is below the safe threshold, or "" when there is enough. Pure
// function.
func DiskSpaceWarning(freeBytes uint64) string {
	if freeBytes >= minFreeDiskBytes {
		return ""
	}
	return fmt.Sprintf("Only %.1f GB of free space is left on the system drive. The SQL engine installation needs about 1.5 GB of free space — free up space if the installation fails.", float64(freeBytes)/(1<<30))
}

// ---- download (cross-platform) ----

// downloadFile downloads url to dest, invoking progress(percent)
// as bytes arrive. The caller verifies the file before running it.
func downloadFile(ctx context.Context, url, dest string, progress func(int)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: Microsoft's server answered %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 256*1024)
	total := resp.ContentLength
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(downloadPercent(done, total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("download interrupted: %w", rerr)
		}
	}
	if total > 0 && done != total {
		return fmt.Errorf("download incomplete: got %d of %d bytes", done, total)
	}
	return nil
}

// ---- the setup job ----

// Step identifiers, in execution order.
const (
	StepIdle             = "idle"
	StepChecking         = "checking"
	StepDownloading      = "downloading"
	StepVerifying        = "verifying"
	StepInstalling       = "installing"
	StepRepairing        = "repairing"
	StepCreatingInstance = "creating_instance"
	StepRestoring        = "restoring"
	StepImporting        = "importing"
	StepDone             = "done"
	StepFailed           = "failed"
)

// StepOrder is the display order of the progress steps (minus the
// terminal states).
var StepOrder = []string{
	StepChecking, StepDownloading, StepVerifying, StepInstalling,
	StepRepairing, StepCreatingInstance, StepRestoring, StepImporting,
}

// JobStatus is the pollable state of a setup job.
type JobStatus struct {
	Running           bool                 `json:"running"`
	Step              string               `json:"step"`
	DownloadPercent   int                  `json:"download_percent"`
	Warnings          []string             `json:"warnings,omitempty"`
	Detail            string               `json:"detail,omitempty"` // live sub-status (e.g. the repair's current action)
	CanRepair         bool                 `json:"can_repair,omitempty"`
	Error             string               `json:"error,omitempty"`
	FailedStep        string               `json:"failed_step,omitempty"`
	FailedStepLabel   string               `json:"failed_step_label,omitempty"`
	Note              string               `json:"note,omitempty"`
	CanRemoveInstance bool                 `json:"can_remove_instance"`
	Report            *legacyimport.Report `json:"report,omitempty"`
}

// StepLabel renders a step identifier as plain English.
func StepLabel(step string) string {
	switch step {
	case StepChecking:
		return "Checking this computer"
	case StepDownloading:
		return "Downloading the SQL engine from Microsoft"
	case StepVerifying:
		return "Verifying the download (Microsoft signature)"
	case StepInstalling:
		return "Installing the SQL engine"
	case StepRepairing:
		return "Repairing the SQL engine installation"
	case StepCreatingInstance:
		return "Creating the NetGold database instance"
	case StepRestoring:
		return "Restoring the backup file"
	case StepImporting:
		return "Importing into NetGold"
	case StepDone:
		return "Done"
	case StepFailed:
		return "Failed"
	}
	return step
}

// Setup runs one engine-setup-and-import job at a time.
type Setup struct {
	Store *store.Store
	// DataDir is NetGold's data directory; the verbose installer log
	// (sqlengine-install.log) is written there so a failed setup can
	// show the user the installer's own explanation. Empty means:
	// keep the log beside the downloaded installer instead.
	DataDir string

	mu     sync.Mutex
	status JobStatus
}

func (s *Setup) setStep(step string) {
	s.mu.Lock()
	s.status.Step = step
	s.mu.Unlock()
}

func (s *Setup) setProgress(p int) {
	s.mu.Lock()
	s.status.DownloadPercent = p
	s.mu.Unlock()
}

func (s *Setup) fail(step string, err error) {
	s.mu.Lock()
	s.status.Running = false
	s.status.FailedStep = step
	s.status.FailedStepLabel = StepLabel(step)
	s.status.Step = StepFailed
	s.status.Error = fmt.Sprintf("%s failed: %v", StepLabel(step), err)
	// An install that failed (the classic 1603) or an engine that is
	// installed but will not start is exactly what the inbuilt repair
	// exists for — flag it so the page can offer "Repair and retry".
	if step == StepInstalling || step == StepCreatingInstance {
		s.status.CanRepair = platSupported()
	}
	s.mu.Unlock()
}

func (s *Setup) setDetail(d string) {
	s.mu.Lock()
	s.status.Detail = d
	s.mu.Unlock()
}

// addNote appends a sentence to the job's note (kept visible in the
// final status).
func (s *Setup) addNote(n string) {
	s.mu.Lock()
	if s.status.Note == "" {
		s.status.Note = n
	} else {
		s.status.Note += " " + n
	}
	s.mu.Unlock()
}

// Status returns a snapshot of the current (or last) job. Before the
// first job ever runs, the step reports "idle".
func (s *Setup) Status() JobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	if st.Step == "" {
		st.Step = StepIdle
	}
	return st
}

// Start validates and launches the job in the background. runningAsService
// must be true when NetGold runs as the Windows service: setup needs an
// interactive desktop for the installer/UAC prompts, so it refuses there.
func (s *Setup) Start(bakPath string, runningAsService bool) error {
	if !platSupported() {
		return errUnsupported()
	}
	if runningAsService {
		return fmt.Errorf("NetGold is running as a Windows service, which cannot show installer or administrator (UAC) prompts. " +
			"Close the service, run the NetGold desktop program once, and start the engine setup from there — afterwards the service can use the installed engine for normal imports.")
	}
	if err := legacyimport.ValidateBakPath(bakPath); err != nil {
		return err
	}
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return fmt.Errorf("an engine setup is already in progress")
	}
	s.status = JobStatus{Running: true, Step: StepChecking}
	s.mu.Unlock()
	go s.run(bakPath)
	return nil
}

func (s *Setup) run(bakPath string) {
	// Step 1: checking. Pre-flight warnings (pending restart, low
	// disk space) are collected here and stay visible in the status
	// for the whole job.
	s.setStep(StepChecking)
	if ws := platPreflightWarnings(); len(ws) > 0 {
		s.mu.Lock()
		s.status.Warnings = ws
		s.mu.Unlock()
	}

	// (a) A stored engine sign-in that still works — from an
	// earlier setup of either engine — needs no setup at all.
	if cfg, ok := s.storedEngineConn(); ok {
		s.finishImport(cfg, bakPath)
		return
	}

	var tried []string

	// Make LocalDB present if it cheaply can be: on a machine
	// whose sign-in is healthy it stays the lightest engine, so it
	// is installed (or repaired by presence) before the heavier
	// Express engine is considered. A signature failure is NOT
	// worked around — a downloaded file that fails the Microsoft
	// signature check ends the job (fail closed).
	exe, installed := platFindLocalDB()
	if !installed {
		if err := s.tryInstallLocalDB(); err != nil {
			if errors.Is(err, errInstallerSignature) {
				s.fail(StepVerifying, err)
				return
			}
			tried = append(tried, "installing the LocalDB engine failed ("+err.Error()+")")
		}
		exe, installed = platFindLocalDB()
	}

	// Probe whether this Windows sign-in session can use Windows
	// Authentication against the LocalDB engine at all: creating
	// the engine's dedicated sign-in depends on exactly that first
	// connection (the v3.0.1 lesson), so the probe decides the
	// LocalDB path's fate before anything is provisioned.
	probe := probeNotRun
	var pipe string
	if installed {
		s.setStep(StepCreatingInstance)
		s.setDetail("Checking whether this Windows sign-in can reach the local engine…")
		if p, err := platEnsureInstance(exe); err == nil {
			pipe = p
			probe = classifySSPIProbe(platProbeSSPI(PipePathFromInfo(p)))
			if probe == probeSSPIBroken {
				tried = append(tried, "the LocalDB engine could not be used (this Windows sign-in session has no usable credentials for it — error 8009030e)")
			}
		} else {
			tried = append(tried, "the LocalDB instance could not be started ("+err.Error()+")")
		}
		s.setDetail("")
	}

	// (b) LocalDB, when its sign-in probe succeeded.
	if chooseEnginePath(engineFacts{LocalDBPresent: installed, Probe: probe}) == pathLocalDB {
		s.runLocalDBImport(exe, pipe, bakPath)
		return
	}

	// (c) SQL Server Express as a service — no SSPI anywhere.
	if err := s.runExpressImport(bakPath); err == nil {
		return
	} else {
		tried = append(tried, "setting up the SQL Server Express engine failed ("+err.Error()+")")
	}

	// Fallback: the probe did not rule the LocalDB route out (it
	// failed inconclusively) — try it after all.
	if installed && fallbackAfterExpressFailure(probe) == fallbackLocalDB {
		s.addNote("The SQL Server Express setup did not complete, so NetGold is using the LocalDB engine instead.")
		s.runLocalDBImport(exe, pipe, bakPath)
		return
	}
	// (d) Nothing worked: say what was tried, and give the remedy.
	s.fail(StepInstalling, fmt.Errorf("%s", terminalEngineError(tried)))
}

// runLocalDBImport is the LocalDB tail of the setup job: ensure the
// instance, resolve the engine sign-in (the provisioned SQL login —
// never Windows Authentication; see enginelogin.go), and run the
// restore + import.
func (s *Setup) runLocalDBImport(exe, pipe, bakPath string) {
	s.setStep(StepCreatingInstance)
	if pipe == "" {
		p, err := platEnsureInstance(exe)
		if err != nil {
			s.fail(StepCreatingInstance, err)
			return
		}
		pipe = p
	}
	s.setDetail("Preparing the engine sign-in…")
	cfg, err := s.ownEngineConn(pipe)
	if err != nil {
		s.fail(StepCreatingInstance, err)
		return
	}
	s.setDetail("")
	s.finishImport(cfg, bakPath)
}

// stageError tags a failure with the job step it belongs to.
type stageError struct {
	step string
	err  error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// errInstallerSignature marks an Authenticode verification failure.
// Such failures are never worked around with another engine path:
// a file that fails the Microsoft signature check ends the job.
var errInstallerSignature = fmt.Errorf("the installer's Microsoft signature check failed")

// tryInstallLocalDB obtains and installs the LocalDB engine (the
// download/bundled + verify + install steps), returning the failure
// instead of failing the job — the caller decides what else to
// try. A verbose installer log is always written (into the data
// dir when configured) so a failure can explain itself from the
// installer's own words.
func (s *Setup) tryInstallLocalDB() error {
	msiPath, cleanup, err := s.acquireMSIResult()
	if err != nil {
		var se *stageError
		if errors.As(err, &se) {
			return se.err
		}
		return err
	}
	defer cleanup()
	s.setStep(StepInstalling)
	logPath := filepath.Join(filepath.Dir(msiPath), "sqlengine-install.log")
	if s.DataDir != "" {
		logPath = filepath.Join(s.DataDir, "sqlengine-install.log")
	}
	code, err := platInstallMSI(msiPath, logPath)
	if err != nil {
		return err
	}
	if fatal, msg := msiExitGuidance(code); fatal {
		if ev := installLogEvidence(logPath); ev != "" {
			msg += "\n" + ev
		}
		return fmt.Errorf("%s", msg)
	} else if msg != "" {
		s.addNote(msg)
	}
	if _, installed := platFindLocalDB(); !installed {
		return fmt.Errorf("the installer finished, but LocalDB was not found afterwards — try restarting NetGold")
	}
	return nil
}

// acquireMSI obtains the LocalDB installer file and fails the job
// on error (the repair flow's contract). See acquireMSIResult.
func (s *Setup) acquireMSI() (msiPath string, cleanup func(), failed bool) {
	msiPath, cleanup, err := s.acquireMSIResult()
	if err != nil {
		var se *stageError
		if errors.As(err, &se) {
			s.fail(se.step, se.err)
		} else {
			s.fail(StepDownloading, err)
		}
		return "", nil, true
	}
	return msiPath, cleanup, false
}

// acquireMSIResult obtains the LocalDB installer file: the pinned
// official download first; if the download fails for any reason,
// the copy bundled next to the program (installed copies carry
// one) is used instead. Either way the file then passes the
// Authenticode gate. Failures are stageError-tagged; a signature
// failure additionally wraps errInstallerSignature.
func (s *Setup) acquireMSIResult() (msiPath string, cleanup func(), err error) {
	s.setStep(StepDownloading)
	dir, err := os.MkdirTemp("", "netgold-sqlengine-")
	if err != nil {
		return "", nil, &stageError{StepDownloading, err}
	}
	cleanup = func() { os.RemoveAll(dir) }
	dlPath := filepath.Join(dir, "SqlLocalDB.msi")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	dlErr := downloadMSI(ctx, dlPath, s.setProgress)
	cancel()
	bundled := platBundledMSIPath()
	switch chooseMSISource(dlErr == nil, fileExistsNonEmpty(bundled)) {
	case sourceDownload:
		msiPath = dlPath
	case sourceBundled:
		s.addNote("The download from Microsoft did not complete, so NetGold is using the SQL engine installer bundled with the program instead — the same official Microsoft file, verified the same way.")
		msiPath = bundled
	default:
		cleanup()
		return "", nil, &stageError{StepDownloading, dlErr}
	}
	// Verifying: Authenticode must be Valid + Microsoft, for the
	// downloaded and the bundled file alike.
	s.setStep(StepVerifying)
	if err := platVerifySignature(msiPath); err != nil {
		cleanup()
		return "", nil, &stageError{StepVerifying, fmt.Errorf("%w: %v", errInstallerSignature, err)}
	}
	return msiPath, cleanup, nil
}

// finishImport runs the restore + import steps through a started
// LocalDB instance and completes the job. cfg is the engine
// connection resolved by ownEngineConn (the provisioned SQL login —
// never Windows Authentication; see enginelogin.go).
func (s *Setup) finishImport(cfg legacyimport.ConnConfig, bakPath string) {
	rep, err := importBakWithProgress(cfg, bakPath, s.Store, func(phase string) {
		switch phase {
		case "restoring":
			s.setStep(StepRestoring)
		case "importing":
			s.setStep(StepImporting)
		}
	})
	if err != nil {
		step := StepRestoring
		s.mu.Lock()
		if s.status.Step == StepImporting {
			step = StepImporting
		}
		s.mu.Unlock()
		s.fail(step, err)
		return
	}
	s.completeJob(rep)
}

// completeJob marks the job done, keeping (and completing) the note.
func (s *Setup) completeJob(rep *legacyimport.Report) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Step = StepDone
	s.status.Report = rep
	s.status.CanRemoveInstance = true
	if s.status.Note == "" {
		s.status.Note = engineKeptNote
	} else if !strings.Contains(s.status.Note, "stays installed") {
		s.status.Note += " " + engineKeptNote
	}
	s.mu.Unlock()
}

// dataDir resolves the directory repair progress/result files live
// in: the job's data dir, falling back to the store's.
func (s *Setup) dataDir() string {
	if s.DataDir != "" {
		return s.DataDir
	}
	if s.Store != nil {
		return s.Store.Dir
	}
	return ""
}

// StartRepair validates and launches a repair-and-retry job: the
// inbuilt residue cleanup + reinstall (one UAC consent), then — when
// bakPath is given — the restore + import continues automatically.
// An empty bakPath repairs the engine only (the page's manual
// "Repair SQL engine" action uses that when no backup is entered).
func (s *Setup) StartRepair(bakPath string, runningAsService bool) error {
	if !platSupported() {
		return errUnsupported()
	}
	if runningAsService {
		return fmt.Errorf("NetGold is running as a Windows service, which cannot show installer or administrator (UAC) prompts. " +
			"Close the service, run the NetGold desktop program once, and start the repair from there.")
	}
	if bakPath != "" {
		if err := legacyimport.ValidateBakPath(bakPath); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return fmt.Errorf("an engine setup is already in progress")
	}
	s.status = JobStatus{Running: true, Step: StepChecking}
	s.mu.Unlock()
	go s.runRepair(bakPath)
	return nil
}

// runRepair is the repair job body: obtain a verified installer,
// run the elevated repair (cleanup + reinstall + instance), then
// continue into the import when a backup path was given.
func (s *Setup) runRepair(bakPath string) {
	s.setStep(StepChecking)
	if ws := platPreflightWarnings(); len(ws) > 0 {
		s.mu.Lock()
		s.status.Warnings = ws
		s.mu.Unlock()
	}
	msiPath, cleanup, failed := s.acquireMSI()
	if failed {
		return
	}
	defer cleanup()

	s.setStep(StepRepairing)
	if err := platRunRepair(msiPath, s.dataDir(), s.setDetail); err != nil {
		s.fail(StepRepairing, err)
		return
	}
	s.setDetail("")

	exe, installed := platFindLocalDB()
	if !installed {
		s.fail(StepCreatingInstance, fmt.Errorf("the repair finished, but LocalDB was still not found — restart NetGold and try again"))
		return
	}
	s.setStep(StepCreatingInstance)
	pipe, err := platEnsureInstance(exe)
	if err != nil {
		s.fail(StepCreatingInstance, err)
		return
	}
	s.addNote("The SQL engine installation was repaired: LocalDB leftovers from earlier setups were removed and the engine was installed cleanly.")
	if bakPath == "" {
		s.completeJob(nil)
		return
	}
	// The repair fixed the LocalDB installation, but whether this
	// session can USE it still depends on its Windows sign-in:
	// provisioning the engine's sign-in needs one SSPI connection.
	// Probe once — when the session's SSPI is broken, continue the
	// import through the Express engine instead (v3.0.2).
	if classifySSPIProbe(platProbeSSPI(PipePathFromInfo(pipe))) == probeSSPIBroken {
		if err := s.runExpressImport(bakPath); err == nil {
			return
		} else {
			s.fail(StepInstalling, fmt.Errorf("%s", terminalEngineError([]string{
				"the LocalDB engine was repaired, but this Windows sign-in session cannot use it — its Windows Authentication has no usable credentials (8009030e)",
				"setting up the SQL Server Express engine failed (" + err.Error() + ")",
			})))
			return
		}
	}
	// The import signs in with the engine's provisioned SQL login,
	// like every import through our own engine (v3.0.1).
	s.setDetail("Preparing the engine sign-in…")
	cfg, err := s.ownEngineConn(pipe)
	if err != nil {
		s.fail(StepCreatingInstance, err)
		return
	}
	s.setDetail("")
	s.finishImport(cfg, bakPath)
}

// sanity: the download URL must stay an HTTPS Microsoft URL for
// SqlLocalDB.msi (guarded here and in tests so an accidental edit is
// caught at build/test time, not on a user's machine).
var _ = func() bool {
	u, err := url.Parse(LocalDBDownloadURL)
	if err != nil || u.Scheme != "https" || u.Host != "download.microsoft.com" ||
		!strings.HasSuffix(u.Path, "/SqlLocalDB.msi") {
		panic("LocalDBDownloadURL must be an https download.microsoft.com URL for SqlLocalDB.msi")
	}
	return true
}()
