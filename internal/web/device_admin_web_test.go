package web

// v3.6 device administration over HTTP: the admin can edit a
// device's IP address (with a clear refusal when the address is
// taken), switch its monitoring off and on, and delete it; the
// viewer is refused every one of those mutations with 403 but may
// still download the device's status timeline as Excel and PDF.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"netgold/internal/poller"
	"netgold/internal/store"
)

func adminServer(t *testing.T) (*Server, http.Handler, *http.Cookie, *http.Cookie) {
	t.Helper()
	s := testServer(t)
	s.Engine = poller.New(s.Store, time.Minute)
	h := setupAdmin(t, s)
	admin, _ := loginAs(t, h, "admin", testAdminPassword)
	rec := doJSON(t, h, http.MethodPut, "/api/settings/access",
		map[string]string{"viewer_id": "noc-view", "viewer_password": "viewer-secret"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable viewer = %d; body: %s", rec.Code, rec.Body.String())
	}
	viewer, code := loginAs(t, h, "noc-view", "viewer-secret")
	if code != http.StatusOK {
		t.Fatalf("viewer login = %d", code)
	}
	return s, h, admin, viewer
}

func TestDeviceAdminHTTP(t *testing.T) {
	s, h, admin, viewer := adminServer(t)
	st := s.Store

	gid, err := st.AddGroup("Site", nil)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := st.AddDevice(store.Device{Name: "Alpha", IP: "10.4.0.1", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := st.AddDevice(store.Device{Name: "Beta", IP: "10.4.0.2", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	_ = id2

	// The viewer is refused every device mutation.
	for _, m := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/devices/1", map[string]any{"name": "Hax"}},
		{http.MethodDelete, "/api/devices/1", nil},
		{http.MethodPost, "/api/topologies/auto/rebuild", nil},
	} {
		path := m.path
		if strings.Contains(path, "/devices/1") {
			path = strings.Replace(path, "/devices/1", "/devices/"+itoa(id1), 1)
		}
		if rec := doJSON(t, h, m.method, path, m.body, viewer); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", m.method, path, rec.Code)
		}
	}

	// Admin edits the IP address: it sticks.
	rec := doJSON(t, h, http.MethodPut, "/api/devices/"+itoa(id1),
		map[string]any{"name": "Alpha", "ip": "10.4.0.11", "type": "router"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin IP edit = %d; body: %s", rec.Code, rec.Body.String())
	}
	d, err := st.GetDevice(id1)
	if err != nil {
		t.Fatal(err)
	}
	if d.IP != "10.4.0.11" {
		t.Errorf("stored IP = %q, want 10.4.0.11", d.IP)
	}
	// Taking Beta's address is refused with a clear message.
	rec = doJSON(t, h, http.MethodPut, "/api/devices/"+itoa(id1),
		map[string]any{"name": "Alpha", "ip": "10.4.0.2", "type": "router"}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate IP edit = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already uses the IP address") {
		t.Errorf("duplicate IP message unclear: %s", rec.Body.String())
	}
	// An invalid address is refused too.
	rec = doJSON(t, h, http.MethodPut, "/api/devices/"+itoa(id1),
		map[string]any{"name": "Alpha", "ip": "not-an-ip", "type": "router"}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid IP edit = %d, want 400", rec.Code)
	}

	// Admin switches monitoring off; the flag round-trips and the
	// device drops out of the down count.
	if _, err := st.DB.Exec(`UPDATE devices SET status='down' WHERE id=?`, id1); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/devices/"+itoa(id1),
		map[string]any{"name": "Alpha", "ip": "10.4.0.11", "type": "router", "monitoring_disabled": true}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable monitoring = %d; body: %s", rec.Code, rec.Body.String())
	}
	var devOut struct {
		MonitoringDisabled bool `json:"monitoring_disabled"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &devOut)
	if !devOut.MonitoringDisabled {
		t.Error("PUT response does not show monitoring_disabled=true")
	}
	rec = doJSON(t, h, http.MethodGet, "/api/stats", nil, admin)
	var stats struct {
		Total    int `json:"total"`
		Down     int `json:"down"`
		Disabled int `json:"disabled"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &stats)
	if stats.Total != 2 || stats.Down != 0 || stats.Disabled != 1 {
		t.Errorf("stats = %+v, want total 2, down 0 (disabled excluded), disabled 1", stats)
	}

	// Admin deletes the device.
	rec = doJSON(t, h, http.MethodDelete, "/api/devices/"+itoa(id1), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete = %d; body: %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetDevice(id1); err == nil {
		t.Error("device still exists after admin DELETE")
	}

	// The rebuild endpoint answers an admin (nothing to build here).
	rec = doJSON(t, h, http.MethodPost, "/api/topologies/auto/rebuild", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin rebuild = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func jsonNumber(id int64) string { return strconv.FormatInt(id, 10) }

func TestTimelineExportHTTP(t *testing.T) {
	s, h, _, viewer := adminServer(t)
	st := s.Store
	id, err := st.AddDevice(store.Device{Name: "Timeline Device", IP: "10.4.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	// Backdate creation so the full history window covers the
	// seeded changes: down, up, down (still down).
	if _, err := st.DB.Exec(`UPDATE devices SET created_at=? WHERE id=?`, now-4000, id); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []struct {
		ts   int64
		kind string
	}{
		{now - 3000, "down"}, {now - 2000, "up"}, {now - 1000, "down"},
	} {
		if err := st.AddEventAt(&id, ev.ts, ev.kind, "seeded"); err != nil {
			t.Fatal(err)
		}
	}

	// The VIEWER may download both formats (read-only downloads).
	rec := doJSON(t, h, http.MethodGet, "/api/devices/"+jsonNumber(id)+"/timeline.xlsx", nil, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer timeline.xlsx = %d; body: %s", rec.Code, rec.Body.String())
	}
	xlsx := rec.Body.Bytes()
	if !bytes.HasPrefix(xlsx, []byte("PK\x03\x04")) {
		t.Fatalf("timeline.xlsx does not start with the zip magic bytes: %q", xlsx[:8])
	}
	f, err := excelize.OpenReader(bytes.NewReader(xlsx))
	if err != nil {
		t.Fatalf("timeline.xlsx does not open as a workbook: %v", err)
	}
	defer f.Close()
	// Newest segment first: the ongoing "down" segment, To = "now".
	state, err := f.GetCellValue("Status Timeline", "C3")
	if err != nil {
		t.Fatal(err)
	}
	if state != "down (ongoing)" {
		t.Errorf("first timeline row state = %q, want %q", state, "down (ongoing)")
	}
	to, err := f.GetCellValue("Status Timeline", "B3")
	if err != nil {
		t.Fatal(err)
	}
	if to != "now" {
		t.Errorf("first timeline row To = %q, want %q", to, "now")
	}
	hdr, err := f.GetCellValue("Status Timeline", "A2")
	if err != nil {
		t.Fatal(err)
	}
	if hdr != "From" {
		t.Errorf("header A2 = %q, want From", hdr)
	}

	rec = doJSON(t, h, http.MethodGet, "/api/devices/"+jsonNumber(id)+"/timeline.pdf", nil, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer timeline.pdf = %d", rec.Code)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF")) {
		t.Errorf("timeline.pdf does not start with %%PDF: %q", rec.Body.Bytes()[:8])
	}
}

func TestTopologyFromMapKeepsParallelLinks(t *testing.T) {
	s, h, admin, _ := adminServer(t)
	st := s.Store
	gid, err := st.AddGroup("Links", nil)
	if err != nil {
		t.Fatal(err)
	}
	d1, err := st.AddDevice(store.Device{Name: "NORTHGATE-DEMO", IP: "10.4.5.1", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := st.AddDevice(store.Device{Name: "SOUTHGATE-DEMO", IP: "10.4.5.2", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := st.UpsertMap(store.Map{SourceMapID: 777, Name: "Parallel Map", GroupID: &gid, MaxX: 400, MaxY: 300})
	if err != nil {
		t.Fatal(err)
	}
	objs := []store.MapObject{
		{MapID: mid, SourceAnnotationID: 1, Type: "Device", DeviceID: &d1, X: 100, Y: 100, W: 32, H: 32},
		{MapID: mid, SourceAnnotationID: 2, Type: "Device", DeviceID: &d2, X: 300, Y: 100, W: 32, H: 32},
		{MapID: mid, SourceAnnotationID: 3, Type: "Line", X: 100, Y: 100, X2: 300, Y2: 100},
		{MapID: mid, SourceAnnotationID: 4, Type: "Line", X: 100, Y: 108, X2: 300, Y2: 108},
	}
	if err := st.ReplaceMapObjects(mid, objs); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/topologies/from-map",
		map[string]any{"map_id": mid}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("from-map = %d; body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		LinksCopied int `json:"links_copied"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.LinksCopied != 2 {
		t.Errorf("links_copied = %d, want 2 (both parallel lines)", out.LinksCopied)
	}
}
