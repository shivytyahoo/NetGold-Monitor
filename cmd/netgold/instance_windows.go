//go:build windows

package main

// Windows halves of the v2.2.2 single-instance design: the
// Toolhelp32 process sweep, the NetGoldMonitor service check, and
// the canonical self-install into %LOCALAPPDATA%\NetGold.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// otherNetGoldPIDs enumerates every running process whose image name
// looks like a NetGold program (netgold*.exe — including browser
// duplicates such as "netgold (1).exe"), excluding this process.
func otherNetGoldPIDs(dataDir string) []int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil
	}
	self := os.Getpid()
	var pids []int
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if int(entry.ProcessID) != self && matchWindowsImageName(name) {
			pids = append(pids, int(entry.ProcessID))
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return pids
}

func killProcesses(pids []int) {
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

// forceKillProcesses: TerminateProcess is already final on Windows,
// so a second sweep is the same call.
func forceKillProcesses(pids []int) { killProcesses(pids) }

// serviceHoldsPort reports whether the NetGoldMonitor Windows service
// is running (or starting). A service is never terminated by the
// takeover path; the user is told how to update it instead.
func serviceHoldsPort() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return false
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return false
	}
	return st.State == svc.Running || st.State == svc.StartPending
}

// canonicalExePath is the one true program location on Windows.
func canonicalExePath() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return canonicalExeFor(base)
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		return canonicalExeFor(cache)
	}
	return ""
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// canonicalizeIfNeeded installs this program to its canonical
// location and relaunches from there, so that from now on there is
// exactly one NetGold program file on the computer — duplicate
// downloads in the Downloads folder stop mattering. It returns true
// when this process should exit because the canonical copy took
// over. Any failure degrades to running in place with a console note.
func canonicalizeIfNeeded(cfg config) bool {
	if cfg.canonicalChild {
		return false
	}
	canonical := canonicalExePath()
	if canonical == "" {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	selfAbs, err := filepath.Abs(self)
	if err != nil || samePath(selfAbs, canonical) {
		return false
	}
	// A copy installed into Program Files by the NetGold installer
	// (v3.0) is already properly installed — relocating it to the
	// per-user location would also separate it from its bundled
	// engine folder beside it. Leave it where the installer put it.
	if pathUnderAnyRoot(selfAbs, []string{
		os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)"),
	}) {
		return false
	}
	if !installCanonical(selfAbs, canonical) {
		fmt.Printf("Note: NetGold Monitor could not install itself to %s — it will run from the current location this time.\n", canonical)
		return false
	}
	createDesktopShortcut(canonical)
	args := append(append([]string{}, os.Args[1:]...), "-canonical-child")
	cmd := exec.Command(canonical, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		fmt.Printf("Note: NetGold Monitor was installed to %s but could not restart from there (%v) — running from the current location.\n", canonical, err)
		return false
	}
	fmt.Printf("Installed NetGold Monitor to %s\n", canonical)
	fmt.Println("From now on, this installed copy is the one that runs — older downloads can be deleted.")
	return true
}

// installCanonical copies the running program file over the
// canonical path. If the installed copy is still running and locks
// the file, every other NetGold process is stopped once and the copy
// retried (the caller's own takeover sweep would do this anyway;
// doing it here lets the install itself succeed).
func installCanonical(self, canonical string) bool {
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		return false
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return false
	}
	tmp := canonical + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return false
	}
	if err := os.Rename(tmp, canonical); err != nil {
		killProcesses(otherNetGoldPIDs(""))
		if err := os.Rename(tmp, canonical); err != nil {
			_ = os.Remove(tmp)
			return false
		}
	}
	return true
}

// createDesktopShortcut drops a "NetGold Monitor" shortcut on the
// Desktop pointing at the installed copy. Best effort: any failure
// is skipped silently.
func createDesktopShortcut(target string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	lnk := filepath.Join(home, "Desktop", "NetGold Monitor.lnk")
	if fileExists(lnk) {
		return
	}
	script := fmt.Sprintf(
		"$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s'); $s.TargetPath='%s'; $s.WorkingDirectory='%s'; $s.Save()",
		psQuote(lnk), psQuote(target), psQuote(filepath.Dir(target)))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	_ = cmd.Run()
}

// psQuote escapes a path for a single-quoted PowerShell string.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
