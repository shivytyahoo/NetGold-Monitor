//go:build !windows

package main

import (
	"fmt"
	"runtime"
)

// runningAsWindowsService is always false off Windows.
func runningAsWindowsService() bool { return false }

// runWindowsService is unreachable off Windows.
func runWindowsService(cfg config) error { return fmt.Errorf("not a Windows build") }

// handleServiceCommand explains the platform-appropriate service setup:
// Windows service control exists only in the Windows build; on Linux a
// systemd unit ships with the source (netgold.service), on macOS a
// launchd plist can wrap the binary the same way.
func handleServiceCommand(cmd string, cfg config) error {
	switch runtime.GOOS {
	case "linux":
		fmt.Println("NetGold on Linux runs as a systemd service, not with -service.")
		fmt.Println("A ready-made unit file, netgold.service, ships with the source package:")
		fmt.Println("  sudo cp netgold.service /etc/systemd/system/")
		fmt.Println("  sudo systemctl daemon-reload && sudo systemctl enable --now netgold")
		fmt.Println("The unit grants CAP_NET_BIND_SERVICE so the syslog receiver can use port 514.")
	case "darwin":
		fmt.Println("NetGold on macOS: wrap the binary in a launchd plist (KeepAlive=true),")
		fmt.Println("or simply run it from a terminal / login item. See README.")
	default:
		fmt.Println("Service control is only available in the Windows build (-service) or via systemd on Linux.")
	}
	return nil
}
