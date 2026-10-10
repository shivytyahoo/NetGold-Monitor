package poller

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"netgold/internal/store"
)

// Regression test for the duplicate transition-event bug at the poller
// level: many concurrent PollDevice calls for the same device (as happen
// when the add-device immediate poll overlaps a ticker poll, or poll-now
// is clicked during a cycle) must produce exactly one event, because
// polls are serialised per device and the store transition is atomic.
// Uses a demo device so no real network traffic is involved.
func TestPollDeviceConcurrentSingleEvent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	id, err := st.AddDevice(store.Device{Name: "demo1", IP: "10.8.8.8", Type: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, time.Minute)
	pollBurst := func(n int) {
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Each caller polls its own snapshot, like the real
				// entry points (ticker list, web handlers) do.
				dev, err := st.GetDevice(id)
				if err != nil {
					return
				}
				e.PollDevice(context.Background(), dev)
			}()
		}
		wg.Wait()
	}
	countEvents := func() int {
		var n int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE device_id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	pollBurst(16)
	if got := countEvents(); got != 1 {
		t.Fatalf("after first concurrent burst: %d events, want exactly 1", got)
	}
	// A second burst while already up must add no further events.
	pollBurst(16)
	if got := countEvents(); got != 1 {
		t.Fatalf("after second concurrent burst: %d events, want still exactly 1", got)
	}
	dev, err := st.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Status != "up" {
		t.Fatalf("status = %q, want up", dev.Status)
	}
}
