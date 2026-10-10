//go:build !windows

package main

// setConsoleTitle: naming the terminal window is a Windows console
// feature; on other platforms there is nothing to do (the startup
// banner already names the version in the terminal output).
func setConsoleTitle() {}

// platformAttachConsole: off Windows the program always runs in the
// terminal it was started from, so there is no console to attach.
func platformAttachConsole() {}

// platformGUIMode: windowless GUI operation (system tray, no
// console) is a Windows feature; Linux and macOS builds keep their
// console behaviour.
func platformGUIMode() bool { return false }
