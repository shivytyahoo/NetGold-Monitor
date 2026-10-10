//go:build windows

package sqlengine

// Windows implementation of the engine repair (see repair.go for the
// plan and the safety rule). Two halves:
//
//   - runRepairElevated (called by the setup job in the normal,
//     unelevated program): relaunches this same program hidden and
//     elevated with -engine-repair, so ONE UAC consent covers the
//     whole cleanup + reinstall, and mirrors the child's progress
//     file into the job status while it waits.
//   - runRepairChild (the elevated copy): performs the steps and
//     reports them through files in the data directory.
//
// The child only ever acts on the allowlisted LocalDB items the pure
// planning functions in repair.go return.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// hideWindowAttr keeps helper processes (msiexec, the repair child)
// from flashing a window — the repair runs invisibly by design.
func hideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

func init() {
	platRunRepair = runRepairElevated
	RunRepairChild = runRepairChild
}

// runRepairElevated starts the elevated repair copy of this program
// and waits for it, forwarding its progress lines to the job.
func runRepairElevated(msiPath, dataDir string, progress func(string)) error {
	return runElevatedChild(
		[]string{"-engine-repair", "-data", dataDir, "-repair-msi", msiPath},
		fmt.Sprintf(`-engine-repair -data "%s" -repair-msi "%s"`, dataDir, msiPath),
		dataDir, RepairProgressFile, RepairResultFile, "repair", progress)
}

// runElevatedChild runs one of this program's hidden helper modes
// (-engine-repair, -engine-provision) with administrator rights and
// mirrors its progress/result files into the caller's progress
// callback. When this process already runs elevated, the child is
// started directly (no second prompt); otherwise it goes through
// the "runas" verb, so exactly one UAC consent covers it. The child
// reports through progressName/resultName files in dataDir; the
// result file's first line is "OK" or "FAILED" (see
// parseRepairResult). kind names the child in error messages
// ("repair", "sign-in helper", …).
func runElevatedChild(directArgs []string, shellParams, dataDir, progressName, resultName, kind string, progress func(string)) error {
	if dataDir == "" {
		return fmt.Errorf("the %s needs NetGold's data directory, but none was configured", kind)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	progressPath := filepath.Join(dataDir, progressName)
	resultPath := filepath.Join(dataDir, resultName)
	// Clear stale reports so the poll below only sees this run.
	_ = os.Remove(progressPath)
	_ = os.Remove(resultPath)

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate the NetGold program file: %w", err)
	}

	if progress != nil {
		progress("Waiting for administrator approval (Windows prompt)…")
	}
	done := make(chan struct{})
	var exitCode uint32
	var runErr error
	go func() {
		defer close(done)
		if processElevated() {
			// Already administrator: the child would only be this
			// same process again — run it directly as a child
			// process (no prompt to wait for).
			cmd := exec.Command(self, directArgs...)
			cmd.SysProcAttr = hideWindowAttr()
			if err := cmd.Run(); err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = uint32(ee.ExitCode())
					return
				}
				runErr = err
				return
			}
			exitCode = 0
			return
		}
		exitCode, runErr = runElevatedAndWait(self, shellParams, true)
	}()

	// Mirror the child's progress file into the job status until
	// the child exits.
	seen := 0
	waiting := true
	for waiting {
		select {
		case <-done:
			// One last read so the final lines are not lost.
			seen = emitProgress(progressPath, seen, progress)
			waiting = false
		default:
			seen = emitProgress(progressPath, seen, progress)
			time.Sleep(300 * time.Millisecond)
		}
	}
	if runErr != nil {
		return runErr
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		return fmt.Errorf("the %s program ended (exit code %d) without leaving a result — see the NetGold log for details", kind, exitCode)
	}
	ok, msg := parseRepairResult(string(data))
	if !ok {
		if msg == "" {
			msg = fmt.Sprintf("the %s did not succeed", kind)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// emitProgress forwards progress-file lines beyond index `seen` to
// the callback and returns the new count.
func emitProgress(path string, seen int, progress func(string)) int {
	if progress == nil {
		return seen
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return seen
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for ; seen < len(lines); seen++ {
		if ln := strings.TrimSpace(lines[seen]); ln != "" {
			progress(ln)
		}
	}
	return seen
}

// ---- the elevated child ----

// repairReporter records one progress line: appended to the progress
// file (for the waiting parent) and kept for the result message.
type repairReporter struct {
	file *os.File
}

func (r *repairReporter) step(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if r.file != nil {
		fmt.Fprintln(r.file, line)
		_ = r.file.Sync()
	}
}

// runRepairChild performs the repair. This process is elevated.
// Its verdict is written to the result file; the error is also
// returned so main can exit non-zero.
func runRepairChild(dataDir, msiPath string) error {
	if dataDir == "" || msiPath == "" {
		return fmt.Errorf("the repair was started without its data directory or installer path")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	pf, _ := os.Create(filepath.Join(dataDir, RepairProgressFile))
	rep := &repairReporter{file: pf}
	if pf != nil {
		defer pf.Close()
	}
	err := doRepair(dataDir, msiPath, rep)
	result := "OK\nThe SQL engine installation was repaired."
	if err != nil {
		result = "FAILED\n" + err.Error()
	}
	_ = os.WriteFile(filepath.Join(dataDir, RepairResultFile), []byte(result), 0o644)
	return err
}

// doRepair executes the repair steps in order. Individual cleanup
// failures are reported and tolerated (residue removal is best
// effort); the install + instance steps are decisive — if they
// fail, the repair fails with the installer's own explanation.
func doRepair(dataDir, msiPath string, rep *repairReporter) error {
	// Step 1: stop and delete every LocalDB instance, when the
	// LocalDB tool is present at all.
	rep.step("Removing LocalDB instances…")
	if exe, ok := findLocalDB(); ok && exe != "" {
		for _, name := range listLocalDBInstances(exe) {
			_, _ = runLocalDB(exe, "stop", name)
			if _, err := runLocalDB(exe, "delete", name); err != nil {
				rep.step("Could not remove instance %q (%v) — continuing.", name, err)
			}
		}
	}

	// Step 2: uninstall every registered LocalDB product (GUIDs are
	// discovered from the registry, never hardcoded).
	rep.step("Uninstalling existing LocalDB products…")
	for _, p := range qualifyingProducts(enumUninstallEntries()) {
		code, err := runMSIUninstall(p.ProductCode)
		if err != nil {
			rep.step("Could not uninstall %q (%v) — continuing.", p.DisplayName, err)
			continue
		}
		// 0/3010 = removed; 1605 = already gone; 1638 = superseded.
		if code != 0 && code != 3010 && code != 1605 && code != 1638 {
			rep.step("Uninstalling %q returned exit code %d — continuing.", p.DisplayName, code)
		}
	}

	// Step 3: back up, then delete, the LocalDB registry keys
	// (the repair.go allowlist — nothing else is touched).
	rep.step("Backing up and removing LocalDB registry keys…")
	backupDir := filepath.Join(dataDir, "repair-backup", time.Now().Format("20060102-150405"))
	_ = os.MkdirAll(backupDir, 0o755)
	for _, k := range repairRegistryKeys {
		full := k.Root + `\` + k.Path
		_ = exec.Command("reg.exe", "export", full,
			filepath.Join(backupDir, k.Root+"-LocalDB.reg"), "/y").Run()
		_ = exec.Command("reg.exe", "delete", full, "/f").Run()
	}

	// Step 4: remove leftover LocalDB folders: the per-user
	// instance files and the LocalDB program folders under EVERY
	// Program Files root (including the real 64-bit root).
	rep.step("Removing leftover LocalDB folders…")
	if folder := perUserInstancesFolder(os.Getenv("LOCALAPPDATA")); folder != "" {
		_ = os.RemoveAll(folder)
	}
	for _, folder := range qualifyingProgramFolders(programFilesRoots()) {
		_ = os.RemoveAll(folder)
	}

	// Step 5: install the verified installer (this process is
	// elevated, so msiexec runs directly).
	rep.step("Installing the SQL engine…")
	logPath := filepath.Join(dataDir, "sqlengine-install.log")
	code, err := runMSIExec(msiPath, logPath)
	if err != nil {
		return err
	}
	if fatal, msg := msiExitGuidance(code); fatal {
		if ev := installLogEvidence(logPath); ev != "" {
			msg += "\n" + ev
		}
		return fmt.Errorf("%s", msg)
	} else if msg != "" {
		rep.step("%s", msg)
	}

	// Step 6: create and start the NetGold instance.
	rep.step("Creating the NetGold instance…")
	exe, ok := findLocalDB()
	if !ok {
		return fmt.Errorf("the installer finished, but LocalDB was not found afterwards — try restarting the computer, then run the repair again")
	}
	if _, err := ensureInstance(exe); err != nil {
		return err
	}
	rep.step("Repair complete.")
	return nil
}

// listLocalDBInstances parses `SqlLocalDB info` (no argument), which
// lists instance names one per line.
func listLocalDBInstances(exe string) []string {
	out, err := runLocalDB(exe, "info")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if n := strings.TrimSpace(line); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// enumUninstallEntries reads both Uninstall registry views (64-bit
// and WOW6432Node) and returns every registered product.
func enumUninstallEntries() []uninstallEntry {
	var out []uninstallEntry
	for _, base := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, base,
			registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, name := range names {
			sub, err := registry.OpenKey(registry.LOCAL_MACHINE, base+`\`+name, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			dn, _, err := sub.GetStringValue("DisplayName")
			sub.Close()
			if err != nil || dn == "" {
				continue
			}
			out = append(out, uninstallEntry{DisplayName: dn, ProductCode: name})
		}
	}
	return out
}

// runMSIUninstall quietly uninstalls one product by code and
// returns the msiexec exit code.
func runMSIUninstall(productCode string) (int, error) {
	cmd := exec.Command("msiexec.exe", "/x", productCode, "/quiet", "/norestart")
	cmd.SysProcAttr = hideWindowAttr()
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, fmt.Errorf("could not start the uninstaller (msiexec): %w", err)
}

// programFilesRoots returns every Program Files root this machine
// can have, the real 64-bit root first: %ProgramW6432% is defined
// even inside a 32-bit process view and always names the true
// 64-bit folder — the path the v2 rescue script missed.
func programFilesRoots() []string {
	return uniqueRoots([]string{
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
	})
}
