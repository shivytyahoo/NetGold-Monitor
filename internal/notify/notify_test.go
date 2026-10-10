package notify

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netgold/internal/store"
)

func testSettings() Settings {
	return Settings{
		Enabled: true, Host: "smtp.example.com", Port: 587, TLSMode: "starttls",
		Username: "alerts@example.com", Password: "secret", From: "alerts@example.com",
		To: "noc@example.com, admin@example.com", OnDown: true, OnUp: true,
	}
}

func TestBuildMessageDown(t *testing.T) {
	s := testSettings()
	d := &store.Device{Name: "Core Router", IP: "10.0.0.1", Type: "router"}
	msg := string(BuildMessage(s, d, "down", 0, time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)))
	for _, want := range []string{
		"From: alerts@example.com",
		"To: noc@example.com, admin@example.com",
		"Subject: [NetGold] DOWN: Core Router (10.0.0.1)",
		"Device Core Router (10.0.0.1) is DOWN.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("down message missing %q\n---\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Downtime:") {
		t.Error("down message must not carry a downtime line")
	}
	if strings.Contains(msg, "secret") {
		t.Error("message must never contain the SMTP password")
	}
}

func TestBuildMessageUpWithDowntime(t *testing.T) {
	s := testSettings()
	d := &store.Device{Name: "District Switch", IP: "10.0.0.2"}
	msg := string(BuildMessage(s, d, "up", 2*time.Hour+5*time.Minute+9*time.Second, time.Now()))
	if !strings.Contains(msg, "Subject: [NetGold] UP: District Switch (10.0.0.2)") {
		t.Errorf("up subject wrong:\n%s", msg)
	}
	if !strings.Contains(msg, "Downtime: 2h 5m 9s") {
		t.Errorf("up message must state the downtime duration:\n%s", msg)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(testSettings()); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	bad := []Settings{
		{Port: 587, TLSMode: "starttls", From: "a@b.c", To: "x@y.z"},                    // no host
		{Host: "h", Port: 0, TLSMode: "starttls", From: "a@b.c", To: "x@y.z"},           // bad port
		{Host: "h", Port: 587, TLSMode: "carrier-pigeon", From: "a@b.c", To: "x@y.z"},   // bad TLS mode
		{Host: "h", Port: 587, TLSMode: "tls", From: "not-an-email", To: "x@y.z"},       // bad from
		{Host: "h", Port: 587, TLSMode: "tls", From: "a@b.c", To: ""},                   // no recipients
		{Host: "h", Port: 587, TLSMode: "tls", From: "a@b.c", To: "noc at example.com"}, // bad recipient
	}
	for i, s := range bad {
		if err := Validate(s); err == nil {
			t.Errorf("case %d: invalid settings accepted: %+v", i, s)
		}
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	s := testSettings()
	if err := Save(st, s); err != nil {
		t.Fatal(err)
	}
	got := Load(st)
	if got != s {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, s)
	}
}

func TestNotifierRespectsToggles(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	s := testSettings()
	s.OnUp = false
	if err := Save(st, s); err != nil {
		t.Fatal(err)
	}
	sent := 0
	n := &Notifier{Store: st, SendFunc: func(Settings, []byte) error { sent++; return nil }}
	d := &store.Device{Name: "R", IP: "10.0.0.1"}
	n.DeviceTransition(d, "down", 0)
	if sent != 1 {
		t.Fatalf("down transition should send, sent=%d", sent)
	}
	n.DeviceTransition(d, "up", time.Minute)
	if sent != 1 {
		t.Fatalf("up transition must be suppressed by the toggle, sent=%d", sent)
	}
	// Disabled entirely: nothing sends.
	s.Enabled = false
	_ = Save(st, s)
	n.DeviceTransition(d, "down", 0)
	if sent != 1 {
		t.Fatalf("disabled notifier sent mail, sent=%d", sent)
	}
}
