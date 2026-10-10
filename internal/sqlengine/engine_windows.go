//go:build windows

package sqlengine

// Windows implementation of the automatic engine setup: locate
// SqlLocalDB.exe (PATH, the LocalDB registry key, then the standard
// Program Files layout), verify installer signatures with PowerShell's
// Get-AuthenticodeSignature, install with msiexec, and drive instances
// with SqlLocalDB.exe (create/start/info/stop/delete).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func init() {
	platSupported = func() bool { return true }
	platFindLocalDB = findLocalDB
	platVerifySignature = verifySignature
	platInstallMSI = installMSI
	platEnsureInstance = ensureInstance
	platInstanceState = instanceState
	platRemoveInstance = removeInstance
	platPreflightWarnings = preflightWarnings
}

// findLocalDB locates SqlLocalDB.exe. Installed means "found".
func findLocalDB() (string, bool) {
	if p, err := exec.LookPath("SqlLocalDB.exe"); err == nil {
		return p, true
	}
	// The registry lists every installed LocalDB version; the tool
	// itself lives in the matching version's Tools\Binn folder.
	if versions := localDBRegistryVersions(); len(versions) > 0 {
		for _, cand := range localDBCandidatePaths() {
			if fileExists(cand) {
				return cand, true
			}
		}
		// Registered as installed but the tool was not found in the
		// usual places: still report installed (the instance commands
		// will surface a precise error if they cannot run).
		return "", true
	}
	for _, cand := range localDBCandidatePaths() {
		if fileExists(cand) {
			return cand, true
		}
	}
	return "", false
}

// localDBRegistryVersions returns the version subkeys under
// HKLM\SOFTWARE\Microsoft\Microsoft SQL Server Local DB\Installed Versions.
func localDBRegistryVersions() []string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Microsoft SQL Server Local DB\Installed Versions`,
		registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	return names
}

// localDBCandidatePaths lists the standard SqlLocalDB.exe locations:
// Program Files\Microsoft SQL Server\<ver>\Tools\Binn, newest first
// (170 = SQL 2025, 160 = 2022, 150 = 2019, 140 = 2017, 130 = 2016,
// 120 = 2014, 110 = 2012).
func localDBCandidatePaths() []string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	var out []string
	for _, v := range []string{"170", "160", "150", "140", "130", "120", "110"} {
		out = append(out, filepath.Join(pf, "Microsoft SQL Server", v, "Tools", "Binn", "SqlLocalDB.exe"))
	}
	return out
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// verifySignature runs the Authenticode check via PowerShell and
// enforces NetGold's policy (Valid + Microsoft) on the parsed facts.
func verifySignature(path string) error {
	// The path is one NetGold generated itself (its own temp folder);
	// single quotes are still escaped for the PowerShell string.
	esc := strings.ReplaceAll(path, "'", "''")
	script := fmt.Sprintf("$s = Get-AuthenticodeSignature -FilePath '%s'; "+
		"Write-Output ('STATUS=' + $s.Status); "+
		"if ($s.SignerCertificate) { Write-Output ('SIGNER=' + $s.SignerCertificate.Subject) }", esc)
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return fmt.Errorf("could not check the installer signature: %w", err)
	}
	status, signer := ParseAuthenticodeOutput(string(out))
	return VerifySignatureResult(status, signer)
}

// installMSI installs the LocalDB MSI and returns its exit code. A
// verbose log is always written to logPath so a failure can explain
// itself. This runs only in the interactive desktop program, never
// in the service.
//
// The install needs administrator rights, and this is where v2.2.3
// went wrong on real machines: a per-machine MSI started as a plain
// child process of an unelevated NetGold with /quiet never shows the
// UAC prompt — Windows Installer simply fails (exit code 1603). So:
// when NetGold itself already runs elevated, msiexec runs directly
// (no second prompt); otherwise the install is launched through
// ShellExecuteEx with the "runas" verb — the user approves the real
// UAC consent prompt, the elevated installer runs, and NetGold waits
// for it and reads its exit code. The Authenticode gate in the job
// has already passed before this is ever called.
func installMSI(msiPath, logPath string) (int, error) {
	if processElevated() {
		return runMSIExec(msiPath, logPath)
	}
	return runMSIExecElevated(msiPath, logPath)
}

// processElevated reports whether this process already runs with an
// elevated (administrator) token.
func processElevated() bool {
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer tok.Close()
	return tok.IsElevated()
}

func runMSIExec(msiPath, logPath string) (int, error) {
	cmd := exec.Command("msiexec.exe", "/i", msiPath, "/quiet", "/norestart",
		"/L*v", logPath, "IACCEPTSQLLOCALDBLICENSETERMS=YES")
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, fmt.Errorf("could not start the installer (msiexec): %w", err)
}

// shellExecuteInfoW mirrors the Win32 SHELLEXECUTEINFOW structure
// (64-bit layout). golang.org/x/sys/windows wraps the simple
// ShellExecute but not ShellExecuteEx, so it is called through a
// lazy shell32 binding — ShellExecuteEx is what returns the launched
// process handle needed to wait for the install and read its code.
type shellExecuteInfoW struct {
	cbSize     uint32
	fMask      uint32
	hwnd       windows.Handle
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	nShow      int32
	hInstApp   windows.Handle
	idList     unsafe.Pointer
	class      *uint16
	hkeyClass  windows.Handle
	hotKey     uint32
	iconOrMon  windows.Handle
	process    windows.Handle
}

const seeMaskNoCloseProcess = 0x00000040

var (
	shell32DLL          = windows.NewLazyDLL("shell32.dll")
	procShellExecuteExW = shell32DLL.NewProc("ShellExecuteExW")
	ole32DLL            = windows.NewLazyDLL("ole32.dll")
	procCoInitializeEx  = ole32DLL.NewProc("CoInitializeEx")
	procCoUninitialize  = ole32DLL.NewProc("CoUninitialize")
)

// runMSIExecElevated starts msiexec elevated via the "runas" verb
// (raising the Windows UAC consent prompt), waits for it to finish,
// and returns its exit code. If the user declines the prompt,
// ShellExecuteExW fails with ERROR_CANCELLED and the returned error
// carries installCancelledMessage.
func runMSIExecElevated(msiPath, logPath string) (int, error) {
	code, err := runElevatedAndWait("msiexec.exe", fmt.Sprintf(
		`/i "%s" /quiet /norestart /L*v "%s" IACCEPTSQLLOCALDBLICENSETERMS=YES`, msiPath, logPath), false)
	if err != nil {
		return -1, err
	}
	return int(code), nil
}

// runElevatedAndWait launches a program with the "runas" verb (one
// Windows UAC consent prompt), waits for it to finish, and returns
// its exit code. Declining the prompt yields an error carrying
// installCancelledMessage. With hidden=true the launched program
// gets SW_HIDE (used by the engine repair's self-invocation, which
// must never flash a window).
func runElevatedAndWait(file, params string, hidden bool) (uint32, error) {
	// ShellExecuteEx is apartment-sensitive: pin the goroutine to
	// its OS thread and initialise COM on it for the call.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	coInit, _, _ := procCoInitializeEx.Call(0, 0x2 /* COINIT_APARTMENTTHREADED */)
	if coInit == 0 || coInit == 1 { // S_OK / S_FALSE
		defer procCoUninitialize.Call()
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return 0, err
	}
	paramsPtr, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return 0, err
	}
	nShow := int32(1) // SW_SHOWNORMAL; msiexec /quiet shows no UI itself
	if hidden {
		nShow = 0 // SW_HIDE
	}
	info := shellExecuteInfoW{
		fMask:      seeMaskNoCloseProcess,
		verb:       verb,
		file:       filePtr,
		parameters: paramsPtr,
		nShow:      nShow,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == windows.ERROR_CANCELLED {
			return 0, fmt.Errorf("%s", installCancelledMessage)
		}
		return 0, fmt.Errorf("could not start with administrator approval: %v", callErr)
	}
	if info.process == 0 {
		return 0, fmt.Errorf("the program was started, but Windows did not return its process handle, so its result cannot be checked")
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, fmt.Errorf("waiting for the elevated program failed: %w", err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, fmt.Errorf("could not read the elevated program's exit code: %w", err)
	}
	return code, nil
}

// ---- pre-flight warnings (Windows) ----

// preflightWarnings checks the conditions that most often make an
// MSI install fail before it starts: a pending Windows restart
// (three independent indicators) and very low free space on the
// system drive. The warnings only inform — the job still tries.
func preflightWarnings() []string {
	var ws []string
	if registryKeyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) {
		ws = append(ws, "A Windows restart is pending (Component Based Servicing). If the installation fails, restart Windows and try again.")
	}
	if registryKeyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		ws = append(ws, "A Windows restart is pending (Windows Update). If the installation fails, restart Windows and try again.")
	}
	if pendingFileRenameOperations() {
		ws = append(ws, "A Windows restart is pending (files are waiting to be replaced on reboot). If the installation fails, restart Windows and try again.")
	}
	if w := systemDriveSpaceWarning(); w != "" {
		ws = append(ws, w)
	}
	return ws
}

func registryKeyExists(path string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// pendingFileRenameOperations reports whether the Session Manager
// has a non-empty PendingFileRenameOperations list (a classic sign
// that a previous setup is waiting for a reboot to complete).
func pendingFileRenameOperations() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	vals, _, err := k.GetStringsValue("PendingFileRenameOperations")
	if err != nil {
		return false
	}
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func systemDriveSpaceWarning() string {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	root, err := windows.UTF16PtrFromString(drive + `\`)
	if err != nil {
		return ""
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(root, &freeToCaller, &total, &totalFree); err != nil {
		return ""
	}
	return DiskSpaceWarning(freeToCaller)
}

// runLocalDB runs SqlLocalDB.exe and returns its combined output.
func runLocalDB(exe string, args ...string) (string, error) {
	if exe == "" {
		return "", fmt.Errorf("SqlLocalDB.exe was not found — the LocalDB installation looks incomplete")
	}
	out, err := exec.Command(exe, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("SqlLocalDB %s failed: %v — %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ensureInstance creates the NetGold instance if needed, starts it,
// and returns its pipe name as printed by SqlLocalDB info.
func ensureInstance(exe string) (string, error) {
	if _, err := runLocalDB(exe, "create", LocalDBInstanceName); err != nil {
		// "already exists" is the expected outcome on later runs.
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return "", err
		}
	}
	if _, err := runLocalDB(exe, "start", LocalDBInstanceName); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already running") {
			return "", err
		}
	}
	out, err := runLocalDB(exe, "info", LocalDBInstanceName)
	if err != nil {
		return "", err
	}
	info := ParseLocalDBInfo(out)
	if info.PipeName == "" {
		return "", fmt.Errorf("the LocalDB instance %q started, but SqlLocalDB did not report a pipe name", LocalDBInstanceName)
	}
	return info.PipeName, nil
}

// instanceState reports the NetGold instance state, or "" when the
// instance does not exist.
func instanceState(exe string) string {
	out, err := runLocalDB(exe, "info", LocalDBInstanceName)
	if err != nil {
		return ""
	}
	return ParseLocalDBInfo(out).State
}

// removeInstance stops and deletes the NetGold instance.
func removeInstance(exe string) error {
	_, stopErr := runLocalDB(exe, "stop", LocalDBInstanceName)
	_ = stopErr // stopping a stopped/absent instance is not fatal here
	if _, err := runLocalDB(exe, "delete", LocalDBInstanceName); err != nil {
		return err
	}
	return nil
}
