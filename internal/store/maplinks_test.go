package store

import (
	"math"
	"testing"
)

func devObj(id int64, devID int64, x, y float64, status string) MapObject {
	return MapObject{ID: id, Type: "Device", DeviceID: &devID, X: x, Y: y, W: 75, H: 75, DeviceStatus: status}
}

func lineObj(id int64, x1, y1, x2, y2 float64) MapObject {
	return MapObject{ID: id, Type: "Line", X: x1, Y: y1, X2: x2, Y2: y2}
}

func TestNearestDevice(t *testing.T) {
	pts := []MapDevicePoint{
		{DeviceID: 1, X: 0, Y: 0},
		{DeviceID: 2, X: 200, Y: 0},
	}
	if got := NearestDevice(10, 5, pts, 75); got != 1 {
		t.Errorf("near device 1: got %d", got)
	}
	if got := NearestDevice(190, 0, pts, 75); got != 2 {
		t.Errorf("near device 2: got %d", got)
	}
	if got := NearestDevice(100, 300, pts, 75); got != 0 {
		t.Errorf("far from all: got %d, want 0", got)
	}
	// Exactly at the radius edge counts as attributed.
	if got := NearestDevice(75, 0, pts, 75); got != 1 {
		t.Errorf("at radius edge: got %d, want 1", got)
	}
	// Just outside does not.
	if got := NearestDevice(76, 0, pts, 75); got != 0 {
		t.Errorf("outside radius: got %d, want 0", got)
	}
	// Nearest wins when two are in range.
	if got := NearestDevice(60, 0, []MapDevicePoint{{DeviceID: 1, X: 0, Y: 0}, {DeviceID: 2, X: 100, Y: 0}}, 75); got != 2 {
		t.Errorf("nearest of two: got %d, want 2", got)
	}
}

func TestResolveMapLinesStates(t *testing.T) {
	objs := []MapObject{
		devObj(1, 10, 0, 0, "up"),
		devObj(2, 11, 300, 0, "up"),
		devObj(3, 12, 600, 0, "down"),
		lineObj(50, 5, 0, 295, 0),       // 10 <-> 11, both up
		lineObj(51, 595, 0, 305, 0),     // 12(down) <-> 11
		lineObj(52, 900, 900, 950, 900), // nothing nearby
		lineObj(53, 10, 0, 900, 900),    // one end on 10 (up), other free
	}
	res := ResolveMapLines(objs)
	if r := res[50]; r.FromDevice != 10 || r.ToDevice != 11 || r.State != "up" {
		t.Errorf("line 50 = %+v, want 10->11 up", r)
	}
	if r := res[51]; r.State != "down" || r.FromDevice == 0 || r.ToDevice == 0 {
		t.Errorf("line 51 = %+v, want resolved + down", r)
	}
	if r := res[52]; r.FromDevice != 0 || r.ToDevice != 0 || r.State != "unknown" {
		t.Errorf("line 52 = %+v, want unresolved unknown", r)
	}
	// Unresolved line keeps its raw geometry.
	if r := res[52]; r.X1 != 900 || r.Y1 != 900 || r.X2 != 950 || r.Y2 != 900 {
		t.Errorf("line 52 geometry changed: %+v", r)
	}
	if r := res[53]; r.State != "up" || (r.FromDevice != 10 && r.ToDevice != 10) {
		t.Errorf("line 53 = %+v, want single-end up", r)
	}
	// Resolved endpoints are clipped to the icon edge (37.5 from centre
	// for a 75-unit icon): the draw point must sit on the icon boundary,
	// not at the centre.
	r := res[50]
	if d := math.Hypot(r.X1-0, r.Y1-0); math.Abs(d-37.5) > 0.01 {
		t.Errorf("line 50 draw start is %.2f from device centre, want 37.5 (icon edge)", d)
	}
}

func TestResolveMapLinesSelfLoopIgnored(t *testing.T) {
	objs := []MapObject{
		devObj(1, 10, 0, 0, "up"),
		lineObj(50, 5, 0, -5, 0), // both ends on device 10
	}
	r := ResolveMapLines(objs)[50]
	if r.FromDevice != 0 || r.ToDevice != 0 {
		t.Errorf("self loop = %+v, want unattributed", r)
	}
}

func TestFillDeviceTypeBackfill(t *testing.T) {
	st := testStore(t)
	id, err := st.AddDevice(Device{Name: "core", IP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FillDeviceType(id, "Juniper MX104", "Juniper MX80.png"); err != nil {
		t.Fatal(err)
	}
	d, err := st.DeviceByIP("10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if d.DeviceType != "Juniper MX104" || d.TypeIcon != "Juniper MX80.png" {
		t.Errorf("after fill: type=%q icon=%q", d.DeviceType, d.TypeIcon)
	}
	if d.IconID != "juniper" {
		t.Errorf("resolved icon = %q, want juniper", d.IconID)
	}
	// First writer wins: a second fill must not overwrite.
	if err := st.FillDeviceType(id, "Router", "Router.png"); err != nil {
		t.Fatal(err)
	}
	d, _ = st.DeviceByIP("10.0.0.1")
	if d.DeviceType != "Juniper MX104" {
		t.Errorf("second fill overwrote: %q", d.DeviceType)
	}
	// But an AddDevice upsert carrying a legacy type DOES refresh it
	// (re-import semantics), while an upsert with no type keeps it.
	if _, err := st.AddDevice(Device{Name: "core", IP: "10.0.0.1", DeviceType: "Router", TypeIcon: "Router.png"}); err != nil {
		t.Fatal(err)
	}
	d, _ = st.DeviceByIP("10.0.0.1")
	if d.DeviceType != "Router" || d.IconID != "router" {
		t.Errorf("upsert refresh: type=%q icon=%q", d.DeviceType, d.IconID)
	}
	if _, err := st.AddDevice(Device{Name: "core", IP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	d, _ = st.DeviceByIP("10.0.0.1")
	if d.DeviceType != "Router" {
		t.Errorf("empty upsert wiped type: %q", d.DeviceType)
	}
}

// Two markers of the SAME stored device (the legacy system devices merged
// on a shared IP keep one marker each) are distinct icons: a line
// drawn between them resolves, attributed to both markers.
func TestResolveMapLinesBetweenTwinMarkers(t *testing.T) {
	dev := int64(7)
	objs := []MapObject{
		{ID: 1, Type: "Device", DeviceID: &dev, X: 0, Y: 0, W: 75, H: 75, DeviceStatus: "up"},
		{ID: 2, Type: "Device", DeviceID: &dev, X: 300, Y: 0, W: 75, H: 75, DeviceStatus: "up"},
		{ID: 3, Type: "Line", X: 10, Y: 0, X2: 290, Y2: 0},
	}
	r := ResolveMapLines(objs)[3]
	if r.FromObject != 1 || r.ToObject != 2 {
		t.Fatalf("twin-marker line objects = %d -> %d, want 1 -> 2", r.FromObject, r.ToObject)
	}
	if r.FromDevice != 7 || r.ToDevice != 7 {
		t.Fatalf("twin-marker line devices = %d -> %d, want 7 -> 7", r.FromDevice, r.ToDevice)
	}
	if r.State != "up" {
		t.Fatalf("twin-marker line state = %q, want up", r.State)
	}
}
