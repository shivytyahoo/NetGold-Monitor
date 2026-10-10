package poller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"netgold/internal/store"
)

// v3.6: a device whose monitoring is switched off is skipped by the
// poller entirely — PollDevice must not ping it, must not write
// samples, and must not touch its stored status or last_poll, even
// for the synthetic demo device type (which needs no network).
func TestPollDeviceSkipsMonitoringDisabled(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()

	id, err := st.AddDevice(store.Device{Name: "Off Device", IP: "127.0.0.1", Type: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	d.MonitoringDisabled = true
	if err := st.UpdateDevice(*d); err != nil {
		t.Fatal(err)
	}

	e := New(st, time.Minute)
	e.PollDevice(context.Background(), d)

	got, err := st.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPoll != 0 {
		t.Errorf("last_poll = %d, want 0 (the device must not be polled at all)", got.LastPoll)
	}
	if got.Status != "unknown" {
		t.Errorf("status = %q, want %q (untouched while monitoring is off)", got.Status, "unknown")
	}
	var samples int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM samples WHERE device_id=?`, id).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 0 {
		t.Errorf("samples = %d, want 0 for a monitoring-disabled device", samples)
	}

	// Re-enabling monitoring resumes polling immediately.
	got.MonitoringDisabled = false
	if err := st.UpdateDevice(*got); err != nil {
		t.Fatal(err)
	}
	e.PollDevice(context.Background(), got)
	after, err := st.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "up" {
		t.Errorf("after re-enable, demo device status = %q, want up", after.Status)
	}
}
