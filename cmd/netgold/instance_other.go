//go:build !windows && !linux

package main

// Fallback platform halves of the v2.2.2 single-instance design
// (macOS and other Unix): there is no /proc to scan, so the running
// copy is found through the pidfile the program records in its data
// directory. Best effort, as designed — if the recorded process is
// gone, nothing is replaced.

import (
	"os"
	"syscall"
)

// otherNetGoldPIDs returns the PID recorded in the data directory's
// pidfile when that process is still alive and is not this process.
func otherNetGoldPIDs(dataDir string) []int {
	pid, ok := readPIDFile(dataDir)
	if !ok || pid == os.Getpid() {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return nil // recorded process is gone
	}
	return []int{pid}
}

func killProcesses(pids []int) {
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
}

func forceKillProcesses(pids []int) {
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	}
}

// serviceHoldsPort: no NetGoldMonitor service exists off Windows.
func serviceHoldsPort() bool { return false }

// canonicalizeIfNeeded: the canonical self-install is a Windows
// behaviour; elsewhere the binary runs where it was put.
func canonicalizeIfNeeded(cfg config) bool { return false }
