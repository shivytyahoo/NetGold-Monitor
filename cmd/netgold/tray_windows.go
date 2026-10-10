//go:build windows

package main

// System tray presence for windowless GUI mode (v3.0), hand-rolled
// on Win32 via golang.org/x/sys/windows and lazy DLL bindings — no
// extra dependency. A hidden message window receives the tray icon's
// notifications: double-click opens the dashboard in the browser, a
// right-click shows the menu (Open / version credit / Exit).

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"netgold/internal/web"
)

var (
	user32DLL   = windows.NewLazySystemDLL("user32.dll")
	shell32DLL2 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassExW = user32DLL.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32DLL.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32DLL.NewProc("DefWindowProcW")
	procGetMessageW      = user32DLL.NewProc("GetMessageW")
	procTranslateMessage = user32DLL.NewProc("TranslateMessage")
	procDispatchMessageW = user32DLL.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32DLL.NewProc("PostQuitMessage")
	procCreatePopupMenu  = user32DLL.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32DLL.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32DLL.NewProc("TrackPopupMenu")
	procSetForegroundWnd = user32DLL.NewProc("SetForegroundWindow")
	procDestroyWindow    = user32DLL.NewProc("DestroyWindow")
	procGetCursorPos     = user32DLL.NewProc("GetCursorPos")
	procExtractIconExW   = shell32DLL2.NewProc("ExtractIconExW")
	procShellNotifyIconW = shell32DLL2.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32DLL2.NewProc("ShellExecuteW")
	procGetModuleHandleW = kernel32DLL.NewProc("GetModuleHandleW")
)

const (
	wmApp           = 0x8000
	wmCommand       = 0x0111
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmDestroy       = 0x0002

	trayCallbackMsg = wmApp + 1

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo = 0x00000001

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGrayed    = 0x00000001
	mfDisabled  = 0x00000002

	tpmLeftAlign = 0x0000
	tpmRightBtn  = 0x0002

	menuOpen = 1
	menuInfo = 2
	menuExit = 3
)

// notifyIconDataW mirrors NOTIFYICONDATAW (Windows 10/11 layout).
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeoutOrVer    uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

// wndClassExW mirrors WNDCLASSEXW.
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// pointL mirrors POINT.
type pointL struct{ x, y int32 }

// trayState is the tray's runtime state, touched only from the
// window-procedure callback (the message-loop thread).
type trayState struct {
	hwnd   windows.Handle
	nid    notifyIconDataW
	url    string
	onExit func()
}

// openBrowser opens a URL in the default browser (ShellExecuteW
// "open"). Used by the tray, by the auto-open on startup, and by
// the already-running case in GUI mode.
func openBrowser(url string) {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return
	}
	_, _, _ = procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)), 0, 0, 1 /* SW_SHOWNORMAL */)
}

// trayAppIcon extracts the program's own icon from the running
// executable's resources (the embedded NetGold icon), falling back
// to the standard application icon when extraction fails.
func trayAppIcon() windows.Handle {
	self, err := os.Executable()
	if err != nil {
		return 0
	}
	p, err := windows.UTF16PtrFromString(self)
	if err != nil {
		return 0
	}
	var large windows.Handle
	r, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&large)), 0, 1)
	if r != 0 && large != 0 {
		return large
	}
	return 0
}

var activeTray *trayState

func trayWndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	t := activeTray
	if t == nil {
		r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
		return r
	}
	switch msg {
	case trayCallbackMsg:
		switch lParam {
		case wmLButtonDblClk:
			openBrowser(t.url)
		case wmRButtonUp:
			t.showMenu()
		}
		return 0
	case wmCommand:
		switch wParam & 0xFFFF {
		case menuOpen:
			openBrowser(t.url)
		case menuExit:
			if t.onExit != nil {
				t.onExit()
			}
			t.removeIcon()
			procPostQuitMessage.Call(0)
		}
		return 0
	case wmDestroy:
		t.removeIcon()
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func (t *trayState) removeIcon() {
	nid := t.nid
	nid.uFlags = 0
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func (t *trayState) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	openText, _ := windows.UTF16PtrFromString("Open NetGold Monitor")
	infoText, _ := windows.UTF16PtrFromString("NetGold Monitor v" + web.Version + " — by Shivam Bhalla")
	exitText, _ := windows.UTF16PtrFromString("Exit NetGold Monitor")
	_, _, _ = procAppendMenuW.Call(menu, mfString, menuOpen, uintptr(unsafe.Pointer(openText)))
	_, _, _ = procAppendMenuW.Call(menu, mfString|mfGrayed|mfDisabled, menuInfo, uintptr(unsafe.Pointer(infoText)))
	_, _, _ = procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	_, _, _ = procAppendMenuW.Call(menu, mfString, menuExit, uintptr(unsafe.Pointer(exitText)))
	var pt pointL
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// SetForegroundWindow is required for the menu to dismiss
	// correctly when the user clicks elsewhere.
	_, _, _ = procSetForegroundWnd.Call(uintptr(t.hwnd))
	_, _, _ = procTrackPopupMenu.Call(menu, tpmLeftAlign|tpmRightBtn,
		uintptr(pt.x), uintptr(pt.y), 0, uintptr(t.hwnd), 0)
}

// startTray installs the tray icon and runs the Win32 message loop
// on the calling (main) goroutine until the user chooses Exit. It
// returns after the loop ends; the caller then waits for the
// application (already told to stop via onExit) to finish.
func startTray(url string, onExit func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, err := windows.UTF16PtrFromString("NetGoldMonitorTray")
	if err != nil {
		return
	}
	wndProc := windows.NewCallback(trayWndProc)
	wc := wndClassExW{
		lpfnWndProc:   wndProc,
		hInstance:     windows.Handle(hInst),
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return
	}
	title, _ := windows.UTF16PtrFromString("NetGold Monitor")
	hwndRaw, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		0, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwndRaw == 0 {
		return
	}
	hwnd := windows.Handle(hwndRaw)

	t := &trayState{hwnd: hwnd, url: url, onExit: onExit}
	activeTray = t
	defer func() { activeTray = nil }()

	icon := trayAppIcon()
	t.nid.cbSize = uint32(unsafe.Sizeof(t.nid))
	t.nid.hWnd = hwnd
	t.nid.uID = 1
	t.nid.uFlags = nifMessage | nifIcon | nifTip | nifInfo
	t.nid.uCallbackMessage = trayCallbackMsg
	t.nid.hIcon = icon
	copyUTF16(t.nid.szTip[:], "NetGold Monitor is running")
	copyUTF16(t.nid.szInfoTitle[:], "NetGold Monitor")
	copyUTF16(t.nid.szInfo[:], "NetGold Monitor is running")
	t.nid.dwInfoFlags = niifInfo
	t.nid.hBalloonIcon = icon
	if r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid))); r == 0 {
		// No tray to serve (e.g. Explorer not running): keep the
		// program alive on the message loop regardless; the web UI
		// is the program.
	}

	// Standard message pump.
	var msg struct {
		hwnd    windows.Handle
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		pt      pointL
	}
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) { // WM_QUIT or error
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	_, _, _ = procDestroyWindow.Call(uintptr(hwnd))
}

// copyUTF16 copies a Go string into a fixed-size UTF-16 field.
func copyUTF16(dst []uint16, s string) {
	u := windows.StringToUTF16(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
	}
	copy(dst, u)
}
