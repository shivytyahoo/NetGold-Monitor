package report

import (
	"testing"

	"netgold/internal/store"
)

// Window-clipping cases for the ranged Status Timeline export
// (v3.6.1): the export endpoints hand BuildTimeline exactly the
// chosen From–To window, so the segments must be clipped to its
// edges — never reaching outside it.

func TestBuildTimelineWindowClipsSegmentStraddlingFrom(t *testing.T) {
	// The device went down at 100 (before the window) and came up
	// at 200. In the window 150..300 the down segment must start
	// at the window's From (150), not at the real change (100).
	segs := BuildTimeline([]store.Event{sev(100, "down"), sev(200, "up")}, 150, 300, 0)
	if len(segs) != 2 {
		t.Fatalf("segments = %v, want 2", segs)
	}
	if segs[0].State != "down" || segs[0].FromTS != 150 || segs[0].ToTS != 200 || segs[0].DurationSec != 50 {
		t.Errorf("first segment = %+v, want down 150..200 (50s, clipped at From)", segs[0])
	}
	if segs[1].State != "up" || segs[1].FromTS != 200 || segs[1].ToTS != 300 {
		t.Errorf("second segment = %+v, want up 200..300", segs[1])
	}
}

func TestBuildTimelineWindowClipsSegmentStraddlingTo(t *testing.T) {
	// The device went down at 100 and stayed down. In the window
	// 50..150 the down segment must end at the window's To (150),
	// not run on to the present.
	segs := BuildTimeline([]store.Event{sev(100, "down")}, 50, 150, 50)
	if len(segs) != 2 {
		t.Fatalf("segments = %v, want 2", segs)
	}
	if segs[0].State != "unknown" || segs[0].FromTS != 50 || segs[0].ToTS != 100 {
		t.Errorf("first segment = %+v, want unknown 50..100", segs[0])
	}
	if segs[1].State != "down" || segs[1].FromTS != 100 || segs[1].ToTS != 150 || segs[1].DurationSec != 50 {
		t.Errorf("second segment = %+v, want down 100..150 (50s, clipped at To)", segs[1])
	}
}

func TestBuildTimelineWindowOutsideSegments(t *testing.T) {
	// A complete down/up excursion lies entirely before the
	// window: it contributes nothing but the state it left behind
	// (up, from the recovery at 200).
	segs := BuildTimeline([]store.Event{sev(100, "down"), sev(200, "up")}, 300, 400, 0)
	if len(segs) != 1 {
		t.Fatalf("segments = %v, want 1", segs)
	}
	if segs[0].State != "up" || segs[0].FromTS != 300 || segs[0].ToTS != 400 {
		t.Errorf("segment = %+v, want up 300..400", segs[0])
	}
	// And a window entirely before any event sees only unknown.
	segs = BuildTimeline([]store.Event{sev(100, "down"), sev(200, "up")}, 10, 90, 0)
	if len(segs) != 1 || segs[0].State != "unknown" || segs[0].FromTS != 10 || segs[0].ToTS != 90 {
		t.Errorf("early window segments = %v, want one unknown 10..90", segs)
	}
}

func TestBuildTimelineEmptyWindowIsFullHistory(t *testing.T) {
	// The export's "no window" case: the endpoints pass the whole
	// span from the device's creation to now, so the segments are
	// the full history — the same list the un-ranged export has
	// always produced.
	events := []store.Event{sev(3000, "down"), sev(5000, "up"), sev(8000, "down")}
	segs := BuildTimeline(events, 1000, 10000, 1000)
	if len(segs) != 4 {
		t.Fatalf("segments = %v, want 4 (unknown, down, up, down)", segs)
	}
	want := []TimelineSegment{
		{State: "unknown", FromTS: 1000, ToTS: 3000},
		{State: "down", FromTS: 3000, ToTS: 5000},
		{State: "up", FromTS: 5000, ToTS: 8000},
		{State: "down", FromTS: 8000, ToTS: 10000, Ongoing: true},
	}
	for i, w := range want {
		got := segs[i]
		if got.State != w.State || got.FromTS != w.FromTS || got.ToTS != w.ToTS || got.Ongoing != w.Ongoing {
			t.Errorf("segment %d = %+v, want %+v", i, got, w)
		}
	}
}
