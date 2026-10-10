//go:build linux

package main

// Linux halves of the v2.2.2 single-instance design: the holder of
// the port is found by scanning /proc for other processes whose
// executable is a NetGold program file. There is no canonical
// self-install on Linux (packages and the systemd unit own their
// paths) — canonicalizeIfNeeded is a no-op and the data directory
// simply defaults to the user config dir.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// otherNetGoldPIDs scans /proc for processes (other than this one)
// running a NetGold executable. The recorded pidfile in the data
// directory is merged in as a fallback for restricted /proc setups.
func otherNetGoldPIDs(dataDir string) []int {
	self := os.Getpid()
	seen := map[int]bool{self: true}
	var pids []int
	add := func(pid int) {
		if pid > 0 && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || seen[pid] {
				continue
			}
			exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
			if err != nil {
				continue
			}
			// A running-then-replaced binary shows as " (deleted)".
			exe = strings.TrimSuffix(exe, " (deleted)")
			if matchUnixExeName(filepath.Base(exe)) {
				add(pid)
			}
		}
	}
	if pid, ok := readPIDFile(dataDir); ok {
		add(pid)
	}
	return pids
}

func signalPIDs(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(sig)
		}
	}
}

// killProcesses asks the old copies to stop (SIGTERM — the program
// shuts down gracefully on it).
func killProcesses(pids []int) { signalPIDs(pids, syscall.SIGTERM) }

// forceKillProcesses is the follow-up when a graceful stop did not
// free the port in time.
func forceKillProcesses(pids []int) { signalPIDs(pids, syscall.SIGKILL) }

// serviceHoldsPort: the service-protection rule applies to the
// Windows NetGoldMonitor service. On Linux, a systemd unit is a
// deliberate administrator setup with its own binary path; this
// mechanism does not second-guess it, so the check is always false.
func serviceHoldsPort() bool { return false }

// canonicalizeIfNeeded: no self-install on Linux.
func canonicalizeIfNeeded(cfg config) bool { return false }
