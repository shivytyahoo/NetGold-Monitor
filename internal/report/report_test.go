package report

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"netgold/internal/store"
)

// Test period: 10 hours starting at t0.
var (
	t0   = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tEnd = t0.Add(10 * time.Hour)
)

const hour = 3600

func ev(id int64, offsetSec int64, kind string) store.Event {
	return store.Event{DeviceID: &id, TS: t0.Unix() + offsetSec, Kind: kind}
}

func dev(id int64) DeviceInfo {
	return DeviceInfo{ID: id, Name: "dev", IP: "10.0.0.1", Group: "Core"}
}

func summary(t *testing.T, rep Report, id int64) DeviceSummary {
	t.Helper()
	for _, d := range rep.Devices {
		if d.DeviceID == id {
			return d
		}
	}
	t.Fatalf("device %d missing from report %+v", id, rep.Devices)
	return DeviceSummary{}
}

func TestSingleClosedOutage(t *testing.T) {
	rep := Compute([]DeviceInfo{dev(1)}, []store.Event{
		ev(1, 2*hour, "down"), ev(1, 5*hour, "up"),
	}, t0, tEnd)
	s := summary(t, rep, 1)
	if s.Outages != 1 || s.TotalDowntimeSec != 3*hour || s.LongestSec != 3*hour {
		t.Fatalf("summary = %+v", s)
	}
	if math.Abs(s.UptimePct-70.0) > 0.001 {
		t.Fatalf("uptime = %v, want 70", s.UptimePct)
	}
	if len(rep.Outages) != 1 {
		t.Fatalf("outages = %+v", rep.Outages)
	}
	o := rep.Outages[0]
	if o.Ongoing || o.Clipped || o.UpTS == nil || *o.UpTS != t0.Unix()+5*hour || o.DownTS != t0.Unix()+2*hour {
		t.Fatalf("outage = %+v", o)
	}
	if s.CurrentlyDown {
		t.Fatal("device should be up at period end")
	}
}

func TestOngoingOutage(t *testing.T) {
	rep := Compute([]DeviceInfo{dev(1)}, []store.Event{ev(1, 8*hour, "down")}, t0, tEnd)
	s := summary(t, rep, 1)
	if s.Outages != 1 || s.TotalDowntimeSec != 2*hour {
		t.Fatalf("summary = %+v", s)
	}
	o := rep.Outages[0]
	if !o.Ongoing || o.UpTS != nil {
		t.Fatalf("outage should be ongoing with nil UpTS: %+v", o)
	}
	if !s.CurrentlyDown {
		t.Fatal("CurrentlyDown should be true")
	}
	if math.Abs(s.UptimePct-80.0) > 0.001 {
		t.Fatalf("uptime = %v, want 80", s.UptimePct)
	}
}

func TestOutageClippedAtPeriodStart(t *testing.T) {
	// Device went down 1h BEFORE the period (the pre-period state event the
	// store returns) and came back 3h into the period.
	rep := Compute([]DeviceInfo{dev(1)}, []store.Event{
		ev(1, -1*hour, "down"), ev(1, 3*hour, "up"),
	}, t0, tEnd)
	if len(rep.Outages) != 1 {
		t.Fatalf("outages = %+v", rep.Outages)
	}
	o := rep.Outages[0]
	if !o.Clipped {
		t.Fatalf("outage should be clipped: %+v", o)
	}
	if o.DownTS != t0.Unix() || o.DurationSec != 3*hour {
		t.Fatalf("clipped outage = %+v, want start=period start, duration 3h", o)
	}
	s := summary(t, rep, 1)
	if math.Abs(s.UptimePct-70.0) > 0.001 {
		t.Fatalf("uptime = %v, want 70", s.UptimePct)
	}
}

func TestMultipleOutagesSum(t *testing.T) {
	rep := Compute([]DeviceInfo{dev(1)}, []store.Event{
		ev(1, 1*hour, "down"), ev(1, 2*hour, "up"),
		ev(1, 4*hour, "down"), ev(1, 5*hour, "up"),
	}, t0, tEnd)
	s := summary(t, rep, 1)
	if s.Outages != 2 {
		t.Fatalf("outages = %d, want 2", s.Outages)
	}
	if s.TotalDowntimeSec != 2*hour {
		t.Fatalf("total downtime = %d, want %d", s.TotalDowntimeSec, 2*hour)
	}
	if s.LongestSec != 1*hour {
		t.Fatalf("longest = %d, want %d", s.LongestSec, 1*hour)
	}
	if math.Abs(s.UptimePct-80.0) > 0.001 {
		t.Fatalf("uptime = %v, want 80", s.UptimePct)
	}
}

func TestNoEventsIs100Percent(t *testing.T) {
	rep := Compute([]DeviceInfo{dev(1)}, nil, t0, tEnd)
	s := summary(t, rep, 1)
	if s.Outages != 0 || s.TotalDowntimeSec != 0 || s.UptimePct != 100 {
		t.Fatalf("summary = %+v, want 0 outages / 100%%", s)
	}
	if rep.Totals.UptimePct != 100 || rep.Totals.Outages != 0 {
		t.Fatalf("totals = %+v", rep.Totals)
	}
}

func TestDeviceCreatedMidPeriod(t *testing.T) {
	d := dev(1)
	d.CreatedAt = t0.Unix() + 5*hour // exists only for the second half
	rep := Compute([]DeviceInfo{d}, []store.Event{
		ev(1, 6*hour, "down"), ev(1, 7*hour, "up"),
	}, t0, tEnd)
	s := summary(t, rep, 1)
	// Effective period 5h, downtime 1h -> 80% uptime.
	if math.Abs(s.UptimePct-80.0) > 0.001 {
		t.Fatalf("uptime = %v, want 80 (period starts at creation)", s.UptimePct)
	}
}

func TestDuplicateEventsIgnored(t *testing.T) {
	rep := Compute([]DeviceInfo{dev(1)}, []store.Event{
		ev(1, 1*hour, "down"), ev(1, 2*hour, "down"), // duplicate down
		ev(1, 3*hour, "up"), ev(1, 4*hour, "up"), // duplicate up
	}, t0, tEnd)
	s := summary(t, rep, 1)
	if s.Outages != 1 || s.TotalDowntimeSec != 2*hour {
		t.Fatalf("summary = %+v, want 1 outage of 2h", s)
	}
}

func sampleReport() Report {
	return Compute([]DeviceInfo{
		{ID: 1, Name: "TestRouter", Group: "Core", IP: "10.0.0.1"},
		{ID: 2, Name: "IdleServer", Group: "Core", IP: "10.0.0.2"},
	}, []store.Event{
		ev(1, 2*hour, "down"), ev(1, 5*hour, "up"),
		ev(1, 9*hour, "down"), // ongoing
	}, t0, tEnd)
}

func TestExcelReadBack(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteExcel(sampleReport(), &buf); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) < 4 || string(b[:2]) != "PK" {
		t.Fatalf("xlsx does not start with PK zip signature (%d bytes)", len(b))
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) != 2 || sheets[0] != "Summary" || sheets[1] != "Outage Details" {
		t.Fatalf("sheets = %v", sheets)
	}
	if v, _ := f.GetCellValue("Summary", "A1"); v != "Device" {
		t.Fatalf("Summary A1 = %q, want header 'Device'", v)
	}
	if v, _ := f.GetCellValue("Summary", "A2"); v != "TestRouter" {
		t.Fatalf("Summary A2 = %q, want first data row device 'TestRouter' (sorted by downtime desc)", v)
	}
	if v, _ := f.GetCellValue("Summary", "D2"); v != "2" {
		t.Fatalf("Summary D2 (outages) = %q, want 2", v)
	}
	if v, _ := f.GetCellValue("Outage Details", "A1"); v != "Device" {
		t.Fatalf("Details A1 = %q", v)
	}
	if v, _ := f.GetCellValue("Outage Details", "G2"); v != "Restored" {
		t.Fatalf("Details G2 (status) = %q, want Restored", v)
	}
	if v, _ := f.GetCellValue("Outage Details", "G3"); v != "Ongoing" {
		t.Fatalf("Details G3 (status) = %q, want Ongoing", v)
	}
}

func TestPDFBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePDF(sampleReport(), &buf); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("pdf does not start with %%PDF (first bytes %q)", b[:min(8, len(b))])
	}
	if len(b) < 1500 {
		t.Fatalf("pdf suspiciously small: %d bytes", len(b))
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{0: "0s", 45: "45s", 750: "12m 30s", 8040: "2h 14m", 277200: "3d 5h"}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}
