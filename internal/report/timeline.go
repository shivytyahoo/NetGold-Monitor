package report

import "netgold/internal/store"

// TimelineSegment is one continuous span of a single device state
// (up, down or unknown) inside a reporting window. The final segment
// of a window is marked Ongoing: the device is still in that state.
type TimelineSegment struct {
	State       string `json:"state"` // up, down, unknown
	FromTS      int64  `json:"from_ts"`
	ToTS        int64  `json:"to_ts"`
	DurationSec int64  `json:"duration_sec"`
	Ongoing     bool   `json:"ongoing"`
}

// BuildTimeline turns one device's up/down state-change events into
// the list of state segments covering the window [from, to].
//
// Semantics, consistent with the downtime report (Compute):
//   - The window is clipped at the device's creation time — the
//     device did not exist before it.
//   - The state at the window start is the state set by the latest
//     state-change event at or before the start (the store's
//     DeviceReportEvents returns that event as its first row). With
//     no such event the state is "unknown" until the first event —
//     the device existed but had not been polled yet.
//   - Events exactly at the window end belong to the next window.
//   - Consecutive events with the same state merge into one segment,
//     and zero-length segments are dropped (except that a window
//     always yields at least one segment).
//   - The last segment runs to the window end and is Ongoing.
//
// events must be ascending by (ts, id) and contain only up/down
// state-change rows for this one device.
func BuildTimeline(events []store.Event, from, to, createdAt int64) []TimelineSegment {
	if to <= from {
		return nil
	}
	start := from
	if createdAt > start {
		start = createdAt
	}
	if start >= to {
		// The device was created at or after the window end.
		return []TimelineSegment{{State: "unknown", FromTS: from, ToTS: to, DurationSec: to - from}}
	}

	state := "unknown"
	segStart := start
	var segs []TimelineSegment
	flush := func(end int64) {
		if end <= segStart && len(segs) > 0 {
			return // zero-length span between two same-instant events
		}
		segs = append(segs, TimelineSegment{State: state, FromTS: segStart, ToTS: end, DurationSec: end - segStart})
	}
	for _, ev := range events {
		if ev.Kind != "up" && ev.Kind != "down" {
			continue
		}
		if ev.TS <= start {
			state = ev.Kind // latest pre-window event wins (ascending order)
			continue
		}
		if ev.TS >= to {
			break
		}
		if ev.Kind == state {
			continue
		}
		flush(ev.TS)
		state = ev.Kind
		segStart = ev.TS
	}
	flush(to)
	if len(segs) == 0 {
		segs = append(segs, TimelineSegment{State: state, FromTS: start, ToTS: to, DurationSec: to - start})
	}
	segs[len(segs)-1].Ongoing = true
	return segs
}

// TimelineUptime returns the uptime percentage implied by segments:
// up time divided by known (up + down) time. Unknown time is
// excluded, matching the downtime report, which cannot count time
// before a device's first recorded state either way. Returns -1
// when the window holds no known up or down time at all.
func TimelineUptime(segs []TimelineSegment) float64 {
	var up, down int64
	for _, s := range segs {
		switch s.State {
		case "up":
			up += s.DurationSec
		case "down":
			down += s.DurationSec
		}
	}
	if up+down == 0 {
		return -1
	}
	return float64(up) / float64(up+down) * 100
}
