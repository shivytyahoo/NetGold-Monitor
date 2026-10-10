//go:build windows

package main

// Windows console title: an interactively started copy names its
// console window "NetGold Monitor v<version>", so the version of
// the copy in a window is visible even in the taskbar. Best effort
// — any failure (no console attached, service mode) is silent.
// golang.org/x/sys/windows has no SetConsoleTitle wrapper, so the
// kernel32 function is called directly.

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"netgold/internal/web"
)

var procSetConsoleTitleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleTitleW")

var kernel32DLL = windows.NewLazySystemDLL("kernel32.dll")
var procAttachConsole = kernel32DLL.NewProc("AttachConsole")

// attachParentProcess is the Win32 ATTACH_PARENT_PROCESS constant.
const attachParentProcess = uintptr(0xFFFFFFFF)

// platformAttachConsole attaches this GUI-subsystem process to the
// console of the terminal that started it, so command-style
// invocations (-service, -version, -help, -engine-repair) can print
// their results. The standard streams are reopened on the console
// devices afterwards: a GUI program starts with no usable console
// handles. Best effort — when there is no parent console (started
// from Explorer) nothing changes.
func platformAttachConsole() {
	r, _, _ := procAttachConsole.Call(attachParentProcess)
	if r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
}

// platformGUIMode: on Windows, an interactive start runs windowless
// (system tray, browser UI, log file) — see main.go.
func platformGUIMode() bool { return true }

func setConsoleTitle() {
	if runningAsWindowsService() {
		return
	}
	title, err := windows.UTF16PtrFromString("NetGold Monitor v" + web.Version)
	if err != nil {
		return
	}
	_, _, _ = procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(title)))
}
