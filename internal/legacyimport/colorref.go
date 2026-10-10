package legacyimport

import (
	"fmt"
	"strconv"
	"strings"
)

// ColorrefToHex converts a Windows COLORREF value as stored by
// the legacy system (e.g. "0x00FF0000") to a CSS hex colour ("#0000FF").
//
// A COLORREF is 0x00BBGGRR: the LOW byte is red, the next is green,
// the next is blue — the reverse of the usual RGB hex order, which is
// why legacy's "0x00FF0000" links are blue, not red, and its "0x00DCFFFF"
// region fill is pale yellow (#FFFFDC), not pale cyan.
func ColorrefToHex(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", false
	}
	t = strings.TrimPrefix(strings.TrimPrefix(t, "0x"), "0X")
	v, err := strconv.ParseUint(t, 16, 32)
	if err != nil {
		return "", false
	}
	r := v & 0xFF
	g := (v >> 8) & 0xFF
	b := (v >> 16) & 0xFF
	return fmt.Sprintf("#%02X%02X%02X", r, g, b), true
}
