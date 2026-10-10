//go:build windows

package legacyimport

// Windows session facts for connection diagnosis (see connerr.go):
// whether this process runs with an elevated token, and whether it
// runs as the SYSTEM account (the Windows service identity). Both
// are read from the process token — no external commands.

import (
	"golang.org/x/sys/windows"
)

func init() {
	platSessionContext = windowsSessionContext
}

func windowsSessionContext() SessionContext {
	var sc SessionContext
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return sc
	}
	defer tok.Close()
	sc.Elevated = tok.IsElevated()
	if u, err := tok.GetTokenUser(); err == nil && u.User.Sid != nil {
		systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
		if err == nil && windows.EqualSid(u.User.Sid, systemSID) {
			sc.System = true
		}
	}
	return sc
}
