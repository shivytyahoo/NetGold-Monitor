package web

// Map editing over HTTP (v3.7): the administrator can place a
// device on a map, create a link between two devices, move a
// marker (its line follows), and remove a marker or a link; the
// viewer is refused every one of those mutations with 403.

import (
	"encoding/json"
	"net/http"
	"testing"

	"netgold/internal/store"
)

func mapEditFixture(t *testing.T) (*Server, http.Handler, *http.Cookie, *http.Cookie, int64, int64, int64, int64) {
	t.Helper()
	s, h, admin, viewer := adminServer(t)
	st := s.Store
	gid, err := st.AddGroup("Web Edit Site", nil)
	if err != nil {
		t.Fatal(err)
	}
	dA, err := st.AddDevice(store.Device{Name: "Web Alpha", IP: "10.8.0.1", GroupID: &gid, Type: "router"})
	if err != nil {
		t.Fatal(err)
	}
	dB, err := st.AddDevice(store.Device{Name: "Web Beta", IP: "10.8.0.2", GroupID: &gid, Type: "switch"})
	if err != nil {
		t.Fatal(err)
	}
	dC, err := st.AddDevice(store.Device{Name: "Web Gamma", IP: "10.8.0.3", GroupID: &gid, Type: "server"})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := st.UpsertMap(store.Map{SourceMapID: 5151, Name: "Web Edit Map", GroupID: &gid,
		MinX: 0, MinY: 0, MaxX: 400, MaxY: 300})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceMapObjects(mid, []store.MapObject{
		{MapID: mid, SourceAnnotationID: 1, Type: "Device", DeviceID: &dA, X: 100, Y: 100, W: 75, H: 75, ZOrder: 2},
		{MapID: mid, SourceAnnotationID: 2, Type: "Line", X: 100, Y: 100, X2: 300, Y2: 100, ZOrder: 1},
		{MapID: mid, SourceAnnotationID: 3, Type: "Device", DeviceID: &dB, X: 300, Y: 100, W: 75, H: 75, ZOrder: 2},
	}); err != nil {
		t.Fatal(err)
	}
	return s, h, admin, viewer, mid, dA, dB, dC
}

func objectsPath(mid int64) string { return "/api/maps/" + itoa(mid) + "/objects" }

func TestMapEditHTTPViewerForbidden(t *testing.T) {
	_, h, _, viewer, mid, _, _, dC := mapEditFixture(t)
	// The viewer is refused all three new mutating endpoints.
	for _, m := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, objectsPath(mid), map[string]any{"type": "Device", "device_id": dC}},
		{http.MethodPut, objectsPath(mid) + "/1", map[string]any{"x": 1, "y": 1}},
		{http.MethodDelete, objectsPath(mid) + "/1", nil},
	} {
		if rec := doJSON(t, h, m.method, m.path, m.body, viewer); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", m.method, m.path, rec.Code)
		}
	}
	// And the viewer can still read the map.
	if rec := doJSON(t, h, http.MethodGet, "/api/maps/"+itoa(mid), nil, viewer); rec.Code != http.StatusOK {
		t.Errorf("viewer GET map = %d, want 200", rec.Code)
	}
}

func TestMapEditHTTPAdminFlow(t *testing.T) {
	s, h, admin, _, mid, dA, dB, dC := mapEditFixture(t)
	st := s.Store

	// Place the off-map device Gamma on the map (default centre).
	rec := doJSON(t, h, http.MethodPost, objectsPath(mid),
		map[string]any{"type": "Device", "device_id": dC}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin add device to map = %d; body: %s", rec.Code, rec.Body.String())
	}
	var gammaObj store.MapObject
	if err := json.Unmarshal(rec.Body.Bytes(), &gammaObj); err != nil {
		t.Fatal(err)
	}
	if gammaObj.X != 200 || gammaObj.Y != 150 {
		t.Errorf("default placement = (%v,%v), want the map centre (200,150)", gammaObj.X, gammaObj.Y)
	}
	// Placing it again conflicts.
	rec = doJSON(t, h, http.MethodPost, objectsPath(mid),
		map[string]any{"type": "Device", "device_id": dC}, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate placement = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	// A link to itself is refused; a link to an off-map device too.
	rec = doJSON(t, h, http.MethodPost, objectsPath(mid),
		map[string]any{"type": "Line", "from_device_id": dA, "to_device_id": dA}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self link = %d, want 400", rec.Code)
	}
	dOff, err := st.AddDevice(store.Device{Name: "Off Map", IP: "10.8.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, h, http.MethodPost, objectsPath(mid),
		map[string]any{"type": "Line", "from_device_id": dA, "to_device_id": dOff}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("link to off-map device = %d, want 400", rec.Code)
	}
	// Link Alpha -> Gamma; its endpoints are the two centres, and a
	// second (parallel) link between the same pair is allowed.
	var linkObj store.MapObject
	for i := 0; i < 2; i++ {
		rec = doJSON(t, h, http.MethodPost, objectsPath(mid),
			map[string]any{"type": "Line", "from_device_id": dA, "to_device_id": dC}, admin)
		if rec.Code != http.StatusCreated {
			t.Fatalf("admin add link #%d = %d; body: %s", i+1, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &linkObj); err != nil {
			t.Fatal(err)
		}
	}
	if linkObj.X != 100 || linkObj.Y != 100 || linkObj.X2 != 200 || linkObj.Y2 != 150 {
		t.Errorf("link endpoints = (%v,%v)-(%v,%v), want the device centres (100,100)-(200,150)",
			linkObj.X, linkObj.Y, linkObj.X2, linkObj.Y2)
	}
	// The map now resolves the new link to Alpha and Gamma.
	rec = doJSON(t, h, http.MethodGet, "/api/maps/"+itoa(mid), nil, admin)
	var got struct {
		Objects []store.MapObject `json:"objects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range got.Objects {
		if o.ID == linkObj.ID && o.LineFromDevice == dA && o.LineToDevice == dC {
			found = true
		}
	}
	if !found {
		t.Error("new link does not resolve to Alpha -> Gamma in GET /api/maps/{id}")
	}

	// Move Alpha far away: the imported line still resolves to it.
	objs, err := st.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	var markerA, importedLine int64
	for _, o := range objs {
		if o.Type == "Device" && o.DeviceID != nil && *o.DeviceID == dA {
			markerA = o.ID
		}
		if o.Type == "Line" && o.SourceAnnotationID == 2 {
			importedLine = o.ID
		}
	}
	rec = doJSON(t, h, http.MethodPut, objectsPath(mid)+"/"+itoa(markerA),
		map[string]any{"x": 1500, "y": 1200}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin move = %d; body: %s", rec.Code, rec.Body.String())
	}
	objs, err = st.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	res := store.ResolveMapLines(objs)
	if r := res[importedLine]; r.FromDevice != dA || r.ToDevice != dB {
		t.Errorf("imported line after move resolves %d -> %d, want %d -> %d", r.FromDevice, r.ToDevice, dA, dB)
	}
	// A non-finite coordinate is refused.
	rec = doJSON(t, h, http.MethodPut, objectsPath(mid)+"/"+itoa(markerA),
		map[string]any{"x": "north", "y": 1}, admin)
	if rec.Code == http.StatusOK {
		t.Error("move with a non-numeric coordinate was accepted")
	}

	// Delete the user link, then remove Gamma from the map: the
	// device itself survives.
	rec = doJSON(t, h, http.MethodDelete, objectsPath(mid)+"/"+itoa(linkObj.ID), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete link = %d; body: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodDelete, objectsPath(mid)+"/"+itoa(gammaObj.ID), nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin remove from map = %d; body: %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetDevice(dC); err != nil {
		t.Errorf("device Gamma gone after remove-from-map: %v", err)
	}
	// Deleting a missing object is a 404.
	rec = doJSON(t, h, http.MethodDelete, objectsPath(mid)+"/"+itoa(gammaObj.ID), nil, admin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second remove = %d, want 404", rec.Code)
	}
	// Unknown map is a 404 as well.
	rec = doJSON(t, h, http.MethodPost, "/api/maps/999999/objects",
		map[string]any{"type": "Device", "device_id": dC}, admin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("add to unknown map = %d, want 404", rec.Code)
	}
}
