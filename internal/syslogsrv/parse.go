// Package syslogsrv receives syslog messages (RFC 3164 and RFC 5424)
// over UDP and TCP and stores them for the Logs page.
package syslogsrv

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"netgold/internal/store"
)

// Parse decodes one syslog message. Malformed input still returns an
// entry (with the raw text as the message) — log collection must never
// choke on a broken sender. recv is the receive time, used for entries
// whose timestamp is missing or year-less.
func Parse(raw string, sourceIP string, recv time.Time) store.SyslogEntry {
	msg := strings.TrimRight(raw, "\r\n\x00")
	e := store.SyslogEntry{
		SourceIP: sourceIP, Facility: 1, Severity: 6,
		TS: recv.Unix(), RecvTS: recv.Unix(),
	}
	rest := msg
	if strings.HasPrefix(rest, "<") {
		if end := strings.IndexByte(rest, '>'); end > 1 && end <= 5 {
			if pri, err := strconv.Atoi(rest[1:end]); err == nil && pri >= 0 && pri <= 191 {
				e.Facility = pri / 8
				e.Severity = pri % 8
				rest = rest[end+1:]
			}
		}
	}
	// RFC 5424: after PRI comes a VERSION digit followed by a space.
	if len(rest) >= 2 && rest[0] >= '1' && rest[0] <= '9' && rest[1] == ' ' {
		if ent, ok := parse5424(e, rest[2:], recv); ok {
			return ent
		}
	}
	return parse3164(e, rest, recv)
}

func parse5424(e store.SyslogEntry, rest string, recv time.Time) (store.SyslogEntry, bool) {
	fields := strings.SplitN(rest, " ", 7)
	if len(fields) < 6 {
		return e, false
	}
	// TIMESTAMP HOSTNAME APP PROCID MSGID SD [MSG]
	if t, err := time.Parse(time.RFC3339Nano, fields[0]); err == nil {
		e.TS = t.Unix()
	}
	if fields[1] != "-" {
		e.Hostname = fields[1]
	}
	if fields[2] != "-" {
		e.App = fields[2]
	}
	rem := fields[6]
	// fields[5] is the structured data: "-" or one or more [..] elements.
	sd := fields[5]
	if sd == "-" {
		e.Message = rem
		return e, true
	}
	// The split above cannot know where SD ends (it can contain spaces
	// inside quoted values), so re-split the whole remainder carefully:
	full := rest
	parts := strings.SplitN(full, " ", 6)
	if len(parts) < 6 {
		e.Message = rem
		return e, true
	}
	sdAndMsg := parts[5]
	if strings.HasPrefix(sdAndMsg, "- ") {
		e.Message = strings.TrimPrefix(sdAndMsg, "- ")
		return e, true
	}
	// Scan SD elements: [ ... ] where ']' inside a quoted value is escaped
	// as '\]'. Find the end of the last consecutive element.
	i := 0
	for i < len(sdAndMsg) && sdAndMsg[i] == '[' {
		j := i + 1
		inQuote := false
		for j < len(sdAndMsg) {
			c := sdAndMsg[j]
			if c == '\\' && j+1 < len(sdAndMsg) {
				j += 2
				continue
			}
			if c == '"' {
				inQuote = !inQuote
			}
			if c == ']' && !inQuote {
				break
			}
			j++
		}
		if j >= len(sdAndMsg) {
			break
		}
		i = j + 1
	}
	msg := strings.TrimPrefix(sdAndMsg[i:], " ")
	// A leading BOM in the MSG part (UTF-8) is common with 5424 senders.
	msg = strings.TrimPrefix(msg, "\ufeff")
	e.Message = msg
	return e, true
}

var months = map[string]time.Month{
	"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}

// parse3164 handles the classic BSD format:
//
//	"Oct  9 05:30:00 host app[pid]: message"
//
// The header is best-effort: if it does not match, whatever follows the
// PRI is stored as the message.
func parse3164(e store.SyslogEntry, rest string, recv time.Time) store.SyslogEntry {
	f := strings.Fields(rest)
	// Timestamp: Mmm dd hh:mm:ss — note the day may be padded ("Oct  9"
	// becomes two fields "Oct", "9" after Fields, same as split).
	if len(f) >= 4 {
		if mon, ok := months[f[0]]; ok {
			if day, err := strconv.Atoi(f[1]); err == nil {
				if t, err := time.Parse("15:04:05", f[2]); err == nil {
					year := recv.Year()
					ts := time.Date(year, mon, day, t.Hour(), t.Minute(), t.Second(), 0, recv.Location())
					if ts.After(recv.Add(24 * time.Hour)) { // message from "the future" = last December seen in January
						ts = time.Date(year-1, mon, day, t.Hour(), t.Minute(), t.Second(), 0, recv.Location())
					}
					e.TS = ts.Unix()
					e.Hostname = f[3]
					// Everything after "time hostname": anchor on the
					// time token so a hostname that also appears earlier
					// in the line cannot confuse the split.
					ti := strings.Index(rest, f[2]) + len(f[2])
					after := strings.TrimSpace(rest[ti:])
					// after begins with the hostname.
					tail := strings.TrimSpace(strings.TrimPrefix(after, f[3]))
					// tail: "tag: message" or "tag[pid]: message"
					if c := strings.Index(tail, ":"); c > 0 && c < 64 && !strings.ContainsAny(tail[:c], " ") {
						tag := tail[:c]
						if b := strings.IndexByte(tag, '['); b >= 0 {
							tag = tag[:b]
						}
						e.App = tag
						e.Message = strings.TrimSpace(tail[c+1:])
						return e
					}
					e.Message = tail
					return e
				}
			}
		}
	}
	// No recognisable header: first token is treated as the hostname if
	// the rest looks like "host message" and the first token has no space
	// (already ensured by Fields).
	e.Message = rest
	return e
}

// Describe is a one-line human summary of an entry (used in CLI tests).
func Describe(e store.SyslogEntry) string {
	return fmt.Sprintf("%s %s %s[%s] %s", time.Unix(e.TS, 0).Format("2006-01-02 15:04:05"),
		e.Hostname, e.App, store.SeverityName(e.Severity), e.Message)
}
