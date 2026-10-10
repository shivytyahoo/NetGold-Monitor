//go:build !windows

package main

// startTray: the system-tray presence is a Windows GUI feature.
// Linux and macOS builds keep their console behaviour, so this is a
// no-op that returns immediately — main.go only calls it in GUI
// mode, which platformGUIMode reports as false off Windows.
func startTray(url string, onExit func()) {}

// openBrowser: only used by the Windows GUI paths (tray, startup
// auto-open, already-running handoff). Off Windows nothing calls
// it, so it is a safe no-op.
func openBrowser(url string) {}
