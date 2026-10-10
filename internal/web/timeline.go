package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"netgold/internal/report"
	"netgold/internal/store"
)

// deviceTimeline serves GET /api/devices/{id}/timeline?range=24h|48h|7d
// (default 48h): the device's status timeline for the device detail
// page — state segments built from its recorded up/down changes —
// plus the uptime percentages for the 24h and 7d windows shown in
// the page header. One 7-day event query feeds all three windows:
// the latest state change inside it also serves as the opening
// state of the shorter windows.
func (s *Server) deviceTimeline(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	dev, err := s.Store.GetDevice(id)
	if err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	rangeName := r.URL.Query().Get("range")
	var span time.Duration
	switch rangeName {
	case "24h":
		span = 24 * time.Hour
	case "7d":
		span = 7 * 24 * time.Hour
	default:
		rangeName, span = "48h", 48*time.Hour
	}
	now := time.Now()
	weekAgo := now.Add(-7 * 24 * time.Hour)
	events, err := s.Store.DeviceReportEvents(id, weekAgo, now)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	build := func(from time.Time) ([]report.TimelineSegment, float64) {
		segs := report.BuildTimeline(events, from.Unix(), now.Unix(), dev.CreatedAt)
		if segs == nil {
			segs = []report.TimelineSegment{}
		}
		return segs, report.TimelineUptime(segs)
	}
	segs, uptime := build(now.Add(-span))
	_, uptime24 := build(now.Add(-24 * time.Hour))
	_, uptime7d := build(weekAgo)
	lastChange := int64(0)
	if n := len(events); n > 0 {
		lastChange = events[n-1].TS
	}
	writeJSON(w, 200, map[string]any{
		"device_id":     id,
		"range":         rangeName,
		"from_ts":       now.Add(-span).Unix(),
		"to_ts":         now.Unix(),
		"current_state": dev.Status,
		"last_change":   lastChange,
		"uptime_pct":    uptime,
		"uptime_24h":    uptime24,
		"uptime_7d":     uptime7d,
		"segments":      segs,
	})
}

// fullTimelineSegments builds the device's status segments over its
// whole recorded history (not just a display window): from the
// device's creation time — or its earliest recorded state change
// when the creation time is unknown — up to now.
func (s *Server) fullTimelineSegments(dev *store.Device) ([]report.TimelineSegment, error) {
	now := time.Now()
	events, err := s.Store.DeviceReportEvents(dev.ID, time.Unix(0, 0), now)
	if err != nil {
		return nil, err
	}
	from := dev.CreatedAt
	if from <= 0 && len(events) > 0 {
		from = events[0].TS
	}
	if from <= 0 {
		from = now.Unix()
	}
	segs := report.BuildTimeline(events, from, now.Unix(), dev.CreatedAt)
	if segs == nil {
		segs = []report.TimelineSegment{}
	}
	return segs, nil
}

// parseExportTime parses one From/To value of the timeline export
// endpoints: a unix timestamp, an RFC 3339 value, or the
// "2006-01-02T15:04" shape a datetime-local input submits
// (interpreted in the server's local time zone).
func parseExportTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, fmt.Errorf("empty time value")
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05", "2006-01-02T15:04",
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time value %q", v)
}

// exportTimelineSegments builds the segments for a timeline export,
// honouring the optional ?from= and ?to= query parameters (v3.6.1).
// With neither parameter the behaviour is the original one: the
// device's full recorded history. A supplied window clips the
// segments to its edges — a state that began before From counts
// from From, and the final segment ends at To (or at the current
// time when To is empty or in the future, in which case it stays
// marked as ongoing). It returns the segments, a plain-English
// label of the exported range for the file header, and — when the
// parameters are invalid — an HTTP status and message instead.
func (s *Server) exportTimelineSegments(dev *store.Device, r *http.Request) (segs []report.TimelineSegment, rangeLabel string, status int, err error) {
	q := r.URL.Query()
	fromQ, toQ := q.Get("from"), q.Get("to")
	if fromQ == "" && toQ == "" {
		segs, err := s.fullTimelineSegments(dev)
		if err != nil {
			return nil, "", 500, err
		}
		return segs, "Full history", 0, nil
	}
	now := time.Now()
	var fromT, toT time.Time
	hasFrom, hasTo := fromQ != "", toQ != ""
	if hasFrom {
		fromT, err = parseExportTime(fromQ)
		if err != nil {
			return nil, "", 400, fmt.Errorf("the From time could not be understood — please use the date and time fields, or clear both fields to export the full history")
		}
	}
	if hasTo {
		toT, err = parseExportTime(toQ)
		if err != nil {
			return nil, "", 400, fmt.Errorf("the To time could not be understood — please use the date and time fields, or clear both fields to export the full history")
		}
	}
	if hasFrom && hasTo && fromT.After(toT) {
		return nil, "", 400, fmt.Errorf("the From time is later than the To time — please choose a From time that is earlier than the To time")
	}
	// The effective window end: To, capped at the current time.
	end := now
	if hasTo && toT.Before(now) {
		end = toT
	}
	// Events are fetched from the window start (or from the
	// beginning of time for an open start); the store also returns
	// the state in force at the start, which is what lets a segment
	// that began before From be clipped to From rather than lost.
	queryFrom := time.Unix(0, 0)
	if hasFrom {
		queryFrom = fromT
	}
	events, err := s.Store.DeviceReportEvents(dev.ID, queryFrom, end)
	if err != nil {
		return nil, "", 500, err
	}
	var startUnix int64
	switch {
	case hasFrom:
		startUnix = fromT.Unix()
	case dev.CreatedAt > 0:
		startUnix = dev.CreatedAt
	case len(events) > 0:
		startUnix = events[0].TS
	default:
		startUnix = end.Unix()
	}
	segs = report.BuildTimeline(events, startUnix, end.Unix(), dev.CreatedAt)
	if segs == nil {
		segs = []report.TimelineSegment{}
	}
	// A window that ends in the past closes its final segment at
	// To: the device may or may not still be in that state now, so
	// the segment must not be presented as ongoing.
	if hasTo && toT.Before(now) && len(segs) > 0 {
		segs[len(segs)-1].Ongoing = false
	}
	startLabel := "the beginning"
	if hasFrom {
		startLabel = fromT.Format("2006-01-02 15:04")
	}
	endLabel := "now"
	if hasTo && !toT.After(now) {
		endLabel = toT.Format("2006-01-02 15:04")
	}
	return segs, startLabel + " to " + endLabel, 0, nil
}

func timelineFileName(dev *store.Device, ext string) string {
	name := strings.Map(func(c rune) rune {
		if c == '"' || c == '/' || c == '\\' {
			return '_'
		}
		return c
	}, dev.Name)
	return fmt.Sprintf("netgold-timeline-%s.%s", name, ext)
}

// GET /api/devices/{id}/timeline.xlsx — the device's status
// timeline as an Excel workbook (v3.6), optionally limited to a
// ?from= / ?to= window (v3.6.1). A read-only download, so viewer
// sessions may use it like the other reports.
func (s *Server) deviceTimelineXLSX(w http.ResponseWriter, r *http.Request) {
	dev, err := s.Store.GetDevice(pathID(r))
	if err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	segs, rangeLabel, status, err := s.exportTimelineSegments(dev, r)
	if err != nil {
		errJSON(w, status, err.Error())
		return
	}
	var buf bytes.Buffer
	if err := report.WriteTimelineExcel(dev.Name, dev.IP, segs, rangeLabel, &buf); err != nil {
		errJSON(w, 500, "excel: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+timelineFileName(dev, "xlsx")+"\"")
	w.Write(buf.Bytes())
}

// GET /api/devices/{id}/timeline.pdf — the same timeline as a
// PDF, with the same optional ?from= / ?to= window as the Excel
// export.
func (s *Server) deviceTimelinePDF(w http.ResponseWriter, r *http.Request) {
	dev, err := s.Store.GetDevice(pathID(r))
	if err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	segs, rangeLabel, status, err := s.exportTimelineSegments(dev, r)
	if err != nil {
		errJSON(w, status, err.Error())
		return
	}
	var buf bytes.Buffer
	if err := report.WriteTimelinePDF(dev.Name, dev.IP, segs, rangeLabel, &buf); err != nil {
		errJSON(w, 500, "pdf: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+timelineFileName(dev, "pdf")+"\"")
	w.Write(buf.Bytes())
}
