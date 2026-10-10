package web

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"netgold/internal/report"
	"netgold/internal/store"
)

// Ranged Status Timeline exports (v3.6.1): the ?from= / ?to=
// window is honoured exactly — segments are clipped to its
// edges, a window in the past closes its final segment instead of
// calling it ongoing, bad input is a clear 400, and viewers may
// download as before.

func seedRangedDevice(t *testing.T, st *store.Store) (int64, int64) {
	t.Helper()
	id, err := st.AddDevice(store.Device{Name: "Ranged Device", IP: "10.4.9.10"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := st.DB.Exec(`UPDATE devices SET created_at=? WHERE id=?`, now-10000, id); err != nil {
		t.Fatal(err)
	}
	// down, up, down (still down): three known segments.
	for _, ev := range []struct {
		ts   int64
		kind string
	}{
		{now - 9000, "down"}, {now - 6000, "up"}, {now - 3000, "down"},
	} {
		if err := st.AddEventAt(&id, ev.ts, ev.kind, "seeded"); err != nil {
			t.Fatal(err)
		}
	}
	return id, now
}

func openTimelineSheet(t *testing.T, body []byte) *excelize.File {
	t.Helper()
	if !bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		t.Fatalf("xlsx does not start with the zip magic bytes: %q", body[:8])
	}
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("xlsx does not open as a workbook: %v", err)
	}
	return f
}

func cell(t *testing.T, f *excelize.File, ref string) string {
	t.Helper()
	v, err := f.GetCellValue("Status Timeline", ref)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTimelineExportWindow(t *testing.T) {
	s, h, _, viewer := adminServer(t)
	id, now := seedRangedDevice(t, s.Store)
	base := "/api/devices/" + strconv.FormatInt(id, 10)

	// A narrow window entirely inside the final down segment:
	// exactly one row, clipped to the window, NOT ongoing.
	from, to := now-2000, now-1000
	url := base + "/timeline.xlsx?from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10)
	rec := doJSON(t, h, http.MethodGet, url, nil, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("windowed xlsx = %d; body: %s", rec.Code, rec.Body.String())
	}
	f := openTimelineSheet(t, rec.Body.Bytes())
	defer f.Close()
	if got := cell(t, f, "A1"); !strings.Contains(got, "Ranged Device") || !strings.Contains(got, "Range:") {
		t.Errorf("title A1 = %q, want the device name and the exported range", got)
	}
	if got := cell(t, f, "C3"); got != "down" {
		t.Errorf("row state = %q, want %q (a past window is not ongoing)", got, "down")
	}
	if got := cell(t, f, "A3"); got != report.FormatTS(from) {
		t.Errorf("row From = %q, want %q (clipped to the window start)", got, report.FormatTS(from))
	}
	if got := cell(t, f, "B3"); got != report.FormatTS(to) {
		t.Errorf("row To = %q, want %q (clipped to the window end)", got, report.FormatTS(to))
	}
	if got := cell(t, f, "A4"); got != "" {
		t.Errorf("unexpected second data row: A4 = %q", got)
	}

	// A window straddling the up -> down change at now-3000: two
	// rows, newest first, both clipped to the window edges.
	from, to = now-4000, now-2000
	url = base + "/timeline.xlsx?from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10)
	rec = doJSON(t, h, http.MethodGet, url, nil, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("straddling xlsx = %d; body: %s", rec.Code, rec.Body.String())
	}
	f2 := openTimelineSheet(t, rec.Body.Bytes())
	defer f2.Close()
	if got := cell(t, f2, "C3"); got != "down" {
		t.Errorf("straddle row 1 state = %q, want down", got)
	}
	if got := cell(t, f2, "A3"); got != report.FormatTS(now-3000) {
		t.Errorf("straddle row 1 From = %q, want %q", got, report.FormatTS(now-3000))
	}
	if got := cell(t, f2, "B3"); got != report.FormatTS(to) {
		t.Errorf("straddle row 1 To = %q, want %q", got, report.FormatTS(to))
	}
	if got := cell(t, f2, "C4"); got != "up" {
		t.Errorf("straddle row 2 state = %q, want up", got)
	}
	if got := cell(t, f2, "A4"); got != report.FormatTS(from) {
		t.Errorf("straddle row 2 From = %q, want %q (clipped to the window start)", got, report.FormatTS(from))
	}

	// The PDF honours the same window and keeps its magic bytes.
	rec = doJSON(t, h, http.MethodGet, base+"/timeline.pdf?from="+strconv.FormatInt(now-2000, 10)+"&to="+strconv.FormatInt(now-1000, 10), nil, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("windowed pdf = %d; body: %s", rec.Code, rec.Body.String())
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF")) {
		t.Errorf("windowed pdf does not start with %%PDF: %q", rec.Body.Bytes()[:8])
	}

	// A To in the future still ends the open segment at "now":
	// the state stays marked ongoing.
	rec = doJSON(t, h, http.MethodGet, base+"/timeline.xlsx?from="+strconv.FormatInt(now-2000, 10)+"&to="+strconv.FormatInt(now+3600, 10), nil, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("future-to xlsx = %d; body: %s", rec.Code, rec.Body.String())
	}
	f3 := openTimelineSheet(t, rec.Body.Bytes())
	defer f3.Close()
	if got := cell(t, f3, "C3"); got != "down (ongoing)" {
		t.Errorf("future-to row state = %q, want %q", got, "down (ongoing)")
	}
	if got := cell(t, f3, "B3"); got != "now" {
		t.Errorf("future-to row To = %q, want %q", got, "now")
	}
}

func TestTimelineExportWindowValidation(t *testing.T) {
	s, h, _, viewer := adminServer(t)
	id, now := seedRangedDevice(t, s.Store)
	base := "/api/devices/" + strconv.FormatInt(id, 10)

	// From after To -> 400 with a plain-English explanation.
	rec := doJSON(t, h, http.MethodGet, base+"/timeline.xlsx?from="+strconv.FormatInt(now-1000, 10)+"&to="+strconv.FormatInt(now-2000, 10), nil, viewer)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("from-after-to = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "From time is later than the To time") {
		t.Errorf("from-after-to body = %s, want a plain-English explanation", rec.Body.String())
	}

	// An unparseable date -> 400 on both endpoints.
	for _, u := range []string{
		base + "/timeline.xlsx?from=not-a-date",
		base + "/timeline.pdf?to=whenever",
	} {
		rec = doJSON(t, h, http.MethodGet, u, nil, viewer)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400; body: %s", u, rec.Code, rec.Body.String())
		}
	}
}
