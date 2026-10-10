package syslogsrv

import (
	"strings"
	"testing"
	"time"
)

var recv = time.Date(2026, 10, 9, 6, 0, 0, 0, time.Local)

func TestParse3164(t *testing.T) {
	e := Parse("<134>Oct  9 05:30:00 core-sw1 sshd[1234]: Accepted password for admin", "10.0.0.5", recv)
	if e.Facility != 16 || e.Severity != 6 {
		t.Errorf("pri: facility=%d severity=%d, want 16/6", e.Facility, e.Severity)
	}
	if e.Hostname != "core-sw1" || e.App != "sshd" {
		t.Errorf("host/app: %q/%q", e.Hostname, e.App)
	}
	if e.Message != "Accepted password for admin" {
		t.Errorf("message: %q", e.Message)
	}
	if e.SourceIP != "10.0.0.5" {
		t.Errorf("source: %q", e.SourceIP)
	}
	want := time.Date(2026, 10, 9, 5, 30, 0, 0, time.Local).Unix()
	if e.TS != want {
		t.Errorf("ts: %d, want %d", e.TS, want)
	}
}

func TestParse3164NoPidAndNoTag(t *testing.T) {
	e := Parse("<131>Oct 9 05:31:00 host2 kernel: link up", "10.0.0.6", recv)
	if e.Severity != 3 || e.Hostname != "host2" || e.App != "kernel" || e.Message != "link up" {
		t.Errorf("got %+v", e)
	}
}

func TestParse5424WithSD(t *testing.T) {
	e := Parse(`<165>1 2026-10-09T05:30:00.123Z core-sw1 netgold 42 ID47 [exampleSDID@32473 iut="3" eventSource="app"] Link down on port 5`, "10.0.0.5", recv)
	if e.Facility != 20 || e.Severity != 5 {
		t.Errorf("pri: facility=%d severity=%d, want 20/5", e.Facility, e.Severity)
	}
	if e.Hostname != "core-sw1" || e.App != "netgold" {
		t.Errorf("host/app: %q/%q", e.Hostname, e.App)
	}
	if e.Message != "Link down on port 5" {
		t.Errorf("message: %q", e.Message)
	}
	want := time.Date(2026, 10, 9, 5, 30, 0, 0, time.UTC).Unix()
	if e.TS != want {
		t.Errorf("ts: %d, want %d", e.TS, want)
	}
}

func TestParse5424NoSD(t *testing.T) {
	e := Parse("<13>1 2026-10-09T05:30:00Z host1 myapp - - - plain message here", "10.0.0.7", recv)
	if e.Hostname != "host1" || e.App != "myapp" || e.Message != "plain message here" {
		t.Errorf("got %+v", e)
	}
	if e.Severity != 5 {
		t.Errorf("severity %d, want 5", e.Severity)
	}
}

func TestParseMalformed(t *testing.T) {
	e := Parse("garbage message with no header at all", "10.0.0.8", recv)
	if e.Message != "garbage message with no header at all" || e.Severity != 6 || e.Facility != 1 {
		t.Errorf("got %+v", e)
	}
	// PRI larger than 191 is invalid and must not be consumed.
	e = Parse("<999999>hello", "10.0.0.8", recv)
	if !strings.HasPrefix(e.Message, "<999999>") {
		t.Errorf("invalid pri consumed: %q", e.Message)
	}
	// Empty input must not panic or invent content.
	e = Parse("", "10.0.0.8", recv)
	if e.Message != "" {
		t.Errorf("empty: %q", e.Message)
	}
}
