//go:build !windows

package legacyimport

// Non-Windows builds have no Windows sign-in sessions to diagnose:
// the session context stays empty (see connerr.go). The default
// platSessionContext already returns that, so nothing is installed
// here.
