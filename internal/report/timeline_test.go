package report

import (
	"testing"

	"netgold/internal/store"
)

func sev(ts int64, kind string) store.Event { return store.Event{TS: ts, Kind: kind} }

func TestBuildTimelineOpenSegmentRunsToNow(t *testing.T) {
	// Window 1000..2000; device went down at 1500 and never recovered:
	// the final segment is open (ongoing) and runs to the window end.
	segs := BuildTimeline([]store.Event{sev(1200, "up"), sev(1500, "down")}, 1000, 2000, 0)
	if len(segs) != 3 {
		t.Fatalf("segments = %v, want 3 (unknown, up, down)", segs)
	}
	if segs[0].State != "unknown" || segs[0].FromTS != 1000 || segs[0].ToTS != 1200 {
		t.Errorf("first segment = %+v, want unknown 1000..1200", segs[0])
	}
	if segs[1].State != "up" || segs[1].FromTS != 1200 || segs[1].ToTS != 1500 {
		t.Errorf("second segment = %+v, want up 1200..1500", segs[1])
	}
	last := segs[2]
	if last.State != "down" || last.FromTS != 1500 || last.ToTS != 2000 || !last.Ongoing || last.DurationSec != 500 {
		t.Errorf("open segment = %+v, want down 1500..2000 ongoing 500s", last)
	}
}

func TestBuildTimelinePrePeriodDown(t *testing.T) {
	// The latest event before the window says the device was already
	// down when the window opened, recovering at 1300.
	segs := BuildTimeline([]store.Event{sev(500, "down"), sev(1300, "up")}, 1000, 2000, 0)
	if len(segs) != 2 {
		t.Fatalf("segments = %v, want 2", segs)
	}
	if segs[0].State != "down" || segs[0].FromTS != 1000 || segs[0].ToTS != 1300 {
		t.Errorf("first segment = %+v, want down 1000..1300", segs[0])
	}
	if got := TimelineUptime(segs); got < 69 || got > 71 {
		t.Errorf("uptime = %.2f, want ~70", got)
	}
}

func TestBuildTimelineCreatedMidPeriod(t *testing.T) {
	// Device created at 1400 inside the window: coverage starts at
	// creation, and with no events yet the state is unknown.
	segs := BuildTimeline(nil, 1000, 2000, 1400)
	if len(segs) != 1 {
		t.Fatalf("segments = %v, want 1", segs)
	}
	if segs[0].State != "unknown" || segs[0].FromTS != 1400 || segs[0].ToTS != 2000 || !segs[0].Ongoing {
		t.Errorf("segment = %+v, want unknown 1400..2000 ongoing", segs[0])
	}
	if got := TimelineUptime(segs); got != -1 {
		t.Errorf("uptime = %.2f, want -1 (no known time)", got)
	}
}

func TestBuildTimelineNoEvents(t *testing.T) {
	// A device that existed for the whole window but was never
	// polled: one unknown segment, no uptime figure.
	segs := BuildTimeline(nil, 1000, 2000, 500)
	if len(segs) != 1 || segs[0].State != "unknown" || segs[0].DurationSec != 1000 {
		t.Fatalf("segments = %v, want one unknown 1000s segment", segs)
	}
}

func TestBuildTimelineIgnoresNonStateEventsAndMerges(t *testing.T) {
	// Info/monitor events are not state changes; repeated same-state
	// events merge; an event exactly at the window end is excluded.
	segs := BuildTimeline([]store.Event{
		sev(1100, "up"), sev(1150, "info"), sev(1200, "up"), sev(2000, "down"),
	}, 1000, 2000, 0)
	if len(segs) != 2 {
		t.Fatalf("segments = %v, want unknown+up", segs)
	}
	if segs[0].State != "unknown" || segs[0].ToTS != 1100 {
		t.Errorf("first segment = %+v, want unknown ..1100", segs[0])
	}
	if segs[1].State != "up" || segs[1].FromTS != 1100 || segs[1].ToTS != 2000 {
		t.Errorf("second segment = %+v, want up 1100..2000", segs[1])
	}
}
