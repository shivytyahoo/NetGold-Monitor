package store

import (
	"errors"
	"math"
	"testing"
)

// Map editing (v3.7): user-created objects take negative source
// annotation ids, a device can be placed only once per map, moving
// a marker carries its attached line endpoints along, removing a
// marker never touches the device, and the stored extents grow to
// contain out-of-bounds placements.

func mapEditFixture(t *testing.T) (*Store, int64, int64, int64, int64) {
	t.Helper()
	s := testStore(t)
	gid, err := s.AddGroup("Edit Site", nil)
	if err != nil {
		t.Fatal(err)
	}
	dA, err := s.AddDevice(Device{Name: "Alpha", IP: "10.7.0.1", GroupID: &gid, Type: "router"})
	if err != nil {
		t.Fatal(err)
	}
	dB, err := s.AddDevice(Device{Name: "Beta", IP: "10.7.0.2", GroupID: &gid, Type: "switch"})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := s.UpsertMap(Map{SourceMapID: 4242, Name: "Edit Map", GroupID: &gid,
		MinX: 0, MinY: 0, MaxX: 400, MaxY: 300})
	if err != nil {
		t.Fatal(err)
	}
	// An imported drawing: two device markers and a line drawn
	// exactly centre to centre.
	objs := []MapObject{
		{MapID: mid, SourceAnnotationID: 11, Type: "Device", DeviceID: &dA, X: 100, Y: 100, W: 75, H: 75, ZOrder: 2},
		{MapID: mid, SourceAnnotationID: 12, Type: "Device", DeviceID: &dB, X: 300, Y: 100, W: 75, H: 75, ZOrder: 2},
		{MapID: mid, SourceAnnotationID: 13, Type: "Line", X: 100, Y: 100, X2: 300, Y2: 100, ZOrder: 1},
	}
	if err := s.ReplaceMapObjects(mid, objs); err != nil {
		t.Fatal(err)
	}
	return s, mid, gid, dA, dB
}

func TestAddMapObjectDevice(t *testing.T) {
	s, mid, gid, dA, _ := mapEditFixture(t)
	dC, err := s.AddDevice(Device{Name: "Gamma", IP: "10.7.0.3", GroupID: &gid, Type: "server"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &dC, X: 200, Y: 220})
	if err != nil {
		t.Fatalf("add device marker: %v", err)
	}
	if added.ID == 0 {
		t.Error("added object has no id")
	}
	if added.SourceAnnotationID != -1 {
		t.Errorf("source annotation id = %d, want -1 (first user-created object)", added.SourceAnnotationID)
	}
	if added.W != 75 || added.H != 75 {
		t.Errorf("marker size = %v x %v, want the imported icon size 75 x 75", added.W, added.H)
	}
	// A second user object takes the next negative id.
	dD, err := s.AddDevice(Device{Name: "Delta", IP: "10.7.0.4", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &dD, X: 60, Y: 240})
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceAnnotationID != -2 {
		t.Errorf("second source annotation id = %d, want -2", second.SourceAnnotationID)
	}
	// The same device cannot be placed twice on one map.
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &dC, X: 10, Y: 10}); !errors.Is(err, ErrDeviceAlreadyOnMap) {
		t.Errorf("duplicate placement err = %v, want ErrDeviceAlreadyOnMap", err)
	}
	// Nor can an imported device be placed again.
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &dA, X: 10, Y: 10}); !errors.Is(err, ErrDeviceAlreadyOnMap) {
		t.Errorf("re-placing imported device err = %v, want ErrDeviceAlreadyOnMap", err)
	}
	// Unknown map / unknown device are reported distinctly.
	if _, err := s.AddMapObject(MapObject{MapID: 99999, Type: "Device", DeviceID: &dC, X: 1, Y: 1}); !errors.Is(err, ErrMapNotFound) {
		t.Errorf("unknown map err = %v, want ErrMapNotFound", err)
	}
	bogus := int64(98765)
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &bogus, X: 1, Y: 1}); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("unknown device err = %v, want ErrDeviceNotFound", err)
	}
	// Non-finite coordinates are refused.
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &dD, X: math.NaN(), Y: 1}); err == nil {
		t.Error("NaN placement was accepted")
	}
	// The stored object round-trips.
	got, err := s.GetMapObject(mid, added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID == nil || *got.DeviceID != dC || got.X != 200 || got.Y != 220 {
		t.Errorf("stored object = %+v, want device %d at (200,220)", got, dC)
	}
}

func TestAddMapObjectLineAndParallel(t *testing.T) {
	s, mid, _, dA, dB := mapEditFixture(t)
	line, err := s.AddMapObject(MapObject{MapID: mid, Type: "Line", X: 100, Y: 100, X2: 300, Y2: 100})
	if err != nil {
		t.Fatalf("add line: %v", err)
	}
	if line.SourceAnnotationID >= 0 {
		t.Errorf("line source annotation id = %d, want a negative user id", line.SourceAnnotationID)
	}
	// A parallel line between the same pair is a second, separate
	// object — never merged away.
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Line", X: 100, Y: 100, X2: 300, Y2: 100}); err != nil {
		t.Fatalf("parallel line refused: %v", err)
	}
	objs, err := s.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, o := range objs {
		if o.Type == "Line" {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("line count = %d, want 3 (1 imported + 2 user-added)", lines)
	}
	// The user-added lines resolve to the same two devices.
	res := ResolveMapLines(objs)
	r, ok := res[line.ID]
	if !ok || r.FromDevice != dA || r.ToDevice != dB {
		t.Errorf("user line resolution = %+v (ok=%v), want devices %d -> %d", r, ok, dA, dB)
	}
	// Unsupported object types are refused.
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Square", X: 1, Y: 1}); err == nil {
		t.Error("adding a Square through the edit API was accepted")
	}
}

func TestMoveMapObjectLineFollows(t *testing.T) {
	s, mid, _, dA, dB := mapEditFixture(t)
	objs, err := s.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	var markerA, lineID int64
	for _, o := range objs {
		if o.Type == "Device" && o.DeviceID != nil && *o.DeviceID == dA {
			markerA = o.ID
		}
		if o.Type == "Line" {
			lineID = o.ID
		}
	}
	// Before the move the line resolves Alpha -> Beta.
	res := ResolveMapLines(objs)
	if r := res[lineID]; r.FromDevice != dA || r.ToDevice != dB {
		t.Fatalf("pre-move resolution = %+v, want %d -> %d", r, dA, dB)
	}
	// Move Alpha far away — far outside the link radius of the
	// line's stored endpoints and outside the map's extents.
	if err := s.MoveMapObject(mid, markerA, 1900, 1400); err != nil {
		t.Fatalf("move: %v", err)
	}
	objs, err = s.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	// The drawn line must still resolve to the same two devices:
	// drawn == stored after the move.
	res = ResolveMapLines(objs)
	r, ok := res[lineID]
	if !ok || r.FromDevice != dA || r.ToDevice != dB {
		t.Fatalf("post-move resolution = %+v (ok=%v), want %d -> %d", r, ok, dA, dB)
	}
	if r.FromObject != markerA {
		t.Errorf("post-move FromObject = %d, want the moved marker %d", r.FromObject, markerA)
	}
	// The line's stored start point is the marker's new centre.
	for _, o := range objs {
		if o.ID == lineID && (o.X != 1900 || o.Y != 1400) {
			t.Errorf("stored line start = (%v,%v), want (1900,1400)", o.X, o.Y)
		}
		if o.ID == markerA && (o.X != 1900 || o.Y != 1400) {
			t.Errorf("stored marker = (%v,%v), want (1900,1400)", o.X, o.Y)
		}
	}
	// The stored extents grew to contain the new position.
	m, err := s.GetMap(mid)
	if err != nil {
		t.Fatal(err)
	}
	if m.MaxX < 1900 || m.MaxY < 1400 {
		t.Errorf("extents after move = (%v,%v)-(%v,%v), want them to contain (1900,1400)",
			m.MinX, m.MinY, m.MaxX, m.MaxY)
	}
	// Moving a line object itself is refused; a missing object and
	// non-finite coordinates are refused too.
	if err := s.MoveMapObject(mid, lineID, 0, 0); err == nil {
		t.Error("moving a Line object was accepted")
	}
	if err := s.MoveMapObject(mid, 987654, 0, 0); !errors.Is(err, ErrMapObjectNotFound) {
		t.Errorf("moving a missing object err = %v, want ErrMapObjectNotFound", err)
	}
	if err := s.MoveMapObject(mid, markerA, math.Inf(1), 0); err == nil {
		t.Error("infinite move was accepted")
	}
}

func TestDeleteMapObjectKeepsDevice(t *testing.T) {
	s, mid, gid, dA, _ := mapEditFixture(t)
	// A second map carrying the same device.
	mid2, err := s.UpsertMap(Map{SourceMapID: 4243, Name: "Other Map", GroupID: &gid, MaxX: 100, MaxY: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceMapObjects(mid2, []MapObject{
		{MapID: mid2, SourceAnnotationID: 21, Type: "Device", DeviceID: &dA, X: 50, Y: 50, W: 75, H: 75},
	}); err != nil {
		t.Fatal(err)
	}
	objs, err := s.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	var markerA int64
	for _, o := range objs {
		if o.Type == "Device" && o.DeviceID != nil && *o.DeviceID == dA {
			markerA = o.ID
		}
	}
	if err := s.DeleteMapObject(mid, markerA); err != nil {
		t.Fatalf("remove from map: %v", err)
	}
	// The device itself survives, with its marker on the other map.
	if _, err := s.GetDevice(dA); err != nil {
		t.Errorf("device gone after remove-from-map: %v", err)
	}
	objs2, err := s.ListMapObjects(mid2)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs2) != 1 {
		t.Errorf("other map objects = %d, want 1 (untouched)", len(objs2))
	}
	objs, err = s.ListMapObjects(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Errorf("edited map objects = %d, want 2 (marker removed, line + other marker kept)", len(objs))
	}
	// Removing the same object again reports it missing.
	if err := s.DeleteMapObject(mid, markerA); !errors.Is(err, ErrMapObjectNotFound) {
		t.Errorf("second remove err = %v, want ErrMapObjectNotFound", err)
	}
}

func TestAddMapObjectExpandsExtents(t *testing.T) {
	s, mid, gid, _, _ := mapEditFixture(t)
	dE, err := s.AddDevice(Device{Name: "Far Away", IP: "10.7.0.9", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMapObject(MapObject{MapID: mid, Type: "Device", DeviceID: &dE, X: -500, Y: 900}); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetMap(mid)
	if err != nil {
		t.Fatal(err)
	}
	if m.MinX > -500 || m.MaxY < 900 {
		t.Errorf("extents = (%v,%v)-(%v,%v), want them to contain (-500,900)", m.MinX, m.MinY, m.MaxX, m.MaxY)
	}
	// Extents never shrink: the original bounds are still inside.
	if m.MaxX < 400 || m.MinY > 0 {
		t.Errorf("extents shrank: (%v,%v)-(%v,%v)", m.MinX, m.MinY, m.MaxX, m.MaxY)
	}
}
