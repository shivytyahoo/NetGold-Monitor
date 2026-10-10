package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}

// Regression (v2.2 live test): Counts on an empty database returned a
// SQL NULL for the SUM columns and failed to scan — /api/stats was a
// 500 on every fresh install before the first device existed.
func TestCountsOnEmptyStore(t *testing.T) {
	s := testStore(t)
	total, up, down, err := s.Counts()
	if err != nil {
		t.Fatalf("counts on empty store: %v", err)
	}
	if total != 0 || up != 0 || down != 0 {
		t.Errorf("empty store counts = %d/%d/%d, want 0/0/0", total, up, down)
	}
}

func TestDeviceAndSamples(t *testing.T) {
	s := testStore(t)
	gid, err := s.AddGroup("Core", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddDevice(Device{Name: "r1", IP: "10.0.0.1", Community: "public", GroupID: &gid, Type: "router"})
	if err != nil {
		t.Fatal(err)
	}
	// duplicate IP upserts, no error
	if _, err := s.AddDevice(Device{Name: "r1-renamed", IP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	devs, _ := s.ListDevices()
	if len(devs) != 1 || devs[0].Name != "r1-renamed" {
		t.Fatalf("devices = %+v", devs)
	}
	if err := s.UpsertInterface(Interface{DeviceID: id, IfIndex: 1, Name: "eth0", SpeedBps: 1e9, OperStatus: "up"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	one := 1
	for i := 0; i < 5; i++ {
		s.AddSample(id, &one, "if_in_bps", now.Add(time.Duration(i)*time.Minute), float64(i*100))
		s.AddSample(id, nil, "ping_ms", now.Add(time.Duration(i)*time.Minute), 1.5)
	}
	got, _ := s.Samples(id, 1, "if_in_bps", now.Add(-time.Hour), now.Add(time.Hour))
	if len(got) != 5 {
		t.Fatalf("if samples = %d", len(got))
	}
	got, _ = s.Samples(id, -2, "ping_ms", now.Add(-time.Hour), now.Add(time.Hour))
	if len(got) != 5 {
		t.Fatalf("ping samples = %d", len(got))
	}
	if v, ok := s.LatestSample(id, 1, "if_in_bps"); !ok || v != 400 {
		t.Fatalf("latest = %v %v", v, ok)
	}
	top, _ := s.TopInterfaces(5)
	if len(top) != 1 {
		t.Fatalf("top = %+v", top)
	}
}

// eventKinds returns the kinds of all events stored for a device, in
// insertion order — the ground truth for the transition tests below.
func eventKinds(t *testing.T, s *Store, deviceID int64) []string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT kind FROM events WHERE device_id=? ORDER BY id`, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// burstTransition fires n concurrent TransitionDeviceStatus calls for the
// same device and status — each goroutine with its own Device snapshot,
// exactly like overlapping poller calls — and returns how many of them
// reported a transition.
func burstTransition(s *Store, id int64, status string, n int) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	transitions := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &Device{ID: id, Name: "race", IP: "10.9.9.9"}
			ok, err := s.TransitionDeviceStatus(d, status, status, "race test is now "+status)
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				transitions++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return transitions
}

// Regression test for the duplicate-transition bug: many concurrent polls
// reporting the same new status must produce exactly ONE event.
func TestTransitionDeviceStatusConcurrentSameStatus(t *testing.T) {
	s := testStore(t)
	id, err := s.AddDevice(Device{Name: "race", IP: "10.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if got := burstTransition(s, id, "down", 32); got != 1 {
		t.Fatalf("concurrent down burst: %d transitions reported, want exactly 1", got)
	}
	if kinds := eventKinds(t, s, id); len(kinds) != 1 || kinds[0] != "down" {
		t.Fatalf("events after down burst = %v, want exactly [down]", kinds)
	}
	dev, err := s.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Status != "down" {
		t.Fatalf("status = %q, want down", dev.Status)
	}
	// Same status again: no transition, no new event.
	if got := burstTransition(s, id, "down", 32); got != 0 {
		t.Fatalf("repeat down burst: %d transitions reported, want 0", got)
	}
	if kinds := eventKinds(t, s, id); len(kinds) != 1 {
		t.Fatalf("events after repeat burst = %v, want still exactly 1", kinds)
	}
	// Recovery burst: exactly one up transition/event.
	if got := burstTransition(s, id, "up", 32); got != 1 {
		t.Fatalf("concurrent up burst: %d transitions reported, want exactly 1", got)
	}
	if kinds := eventKinds(t, s, id); len(kinds) != 2 || kinds[0] != "down" || kinds[1] != "up" {
		t.Fatalf("events after up burst = %v, want [down up]", kinds)
	}
}

// Alternating concurrent bursts must yield a strictly alternating event
// sequence — no duplicated kind can ever appear back to back.
func TestTransitionDeviceStatusAlternating(t *testing.T) {
	s := testStore(t)
	id, err := s.AddDevice(Device{Name: "alt", IP: "10.9.9.10"})
	if err != nil {
		t.Fatal(err)
	}
	const rounds = 6
	for r := 0; r < rounds; r++ {
		status := "down"
		if r%2 == 1 {
			status = "up"
		}
		if got := burstTransition(s, id, status, 16); got != 1 {
			t.Fatalf("round %d (%s): %d transitions, want exactly 1", r, status, got)
		}
	}
	kinds := eventKinds(t, s, id)
	if len(kinds) != rounds {
		t.Fatalf("events = %v, want %d events", kinds, rounds)
	}
	for i, k := range kinds {
		want := "down"
		if i%2 == 1 {
			want = "up"
		}
		if k != want {
			t.Fatalf("events = %v, position %d is %q, want %q (strict alternation)", kinds, i, k, want)
		}
	}
}

func TestDownsample(t *testing.T) {
	s := testStore(t)
	id, _ := s.AddDevice(Device{Name: "r1", IP: "10.0.0.2"})
	// Align to a 5-minute bucket boundary so all 10 samples share one bucket
	// regardless of the wall-clock time the test runs at.
	bucket := (time.Now().Add(-8*24*time.Hour).Unix() / 300) * 300
	old := time.Unix(bucket+10, 0)
	for i := 0; i < 10; i++ { // 10 samples inside one 5-min bucket, values 0..9 -> avg 4.5
		s.AddSample(id, nil, "ping_ms", old.Add(time.Duration(i)*10*time.Second), float64(i))
	}
	if err := s.Downsample(time.Now(), 7*24*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Samples(id, -2, "ping_ms", old.Add(-time.Hour), old.Add(time.Hour))
	if len(got) != 1 || got[0].Value != 4.5 {
		t.Fatalf("after downsample = %+v", got)
	}
	// idempotent
	s.Downsample(old.Add(24*time.Hour), 0, 90*24*time.Hour)
	got, _ = s.Samples(id, -2, "ping_ms", old.Add(-time.Hour), old.Add(time.Hour))
	if len(got) != 1 || got[0].Value != 4.5 {
		t.Fatalf("after second downsample = %+v", got)
	}
}
