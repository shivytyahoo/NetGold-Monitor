package store

import (
	"path/filepath"
	"testing"
)

// v3.6 device administration: the monitoring on/off switch round-trips
// through UpdateDevice, the IP address is editable (and its UNIQUE
// constraint still bites), and DeleteDevice cascades to every table
// that references the device.

func TestUpdateDeviceMonitoringAndIP(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()

	id, err := st.AddDevice(Device{Name: "Core A", IP: "10.0.0.1", Type: "router"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(Device{Name: "Core B", IP: "10.0.0.2", Type: "router"}); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.MonitoringDisabled {
		t.Fatal("a new device must start with monitoring enabled")
	}
	d.MonitoringDisabled = true
	d.IP = "10.0.0.9"
	if err := st.UpdateDevice(*d); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDevice(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MonitoringDisabled {
		t.Error("monitoring_disabled was not stored")
	}
	if got.IP != "10.0.0.9" {
		t.Errorf("IP edit was not stored: got %q", got.IP)
	}
	// Taking another device's IP must fail (UNIQUE constraint).
	got.IP = "10.0.0.2"
	if err := st.UpdateDevice(*got); err == nil {
		t.Error("expected a UNIQUE constraint error when taking another device's IP")
	}
	// Counts: the disabled device is excluded from up/down and
	// reported separately.
	if _, err := st.DB.Exec(`UPDATE devices SET status='down'`); err != nil {
		t.Fatal(err)
	}
	total, up, down, disabled, err := st.CountsDetailed()
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || up != 0 || down != 1 || disabled != 1 {
		t.Errorf("CountsDetailed = total %d up %d down %d disabled %d, want 2/0/1/1", total, up, down, disabled)
	}
}

func TestDeleteDeviceCascade(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()

	gid, err := st.AddGroup("Site", nil)
	if err != nil {
		t.Fatal(err)
	}
	victim, err := st.AddDevice(Device{Name: "Victim", IP: "10.1.0.1", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := st.AddDevice(Device{Name: "Keeper", IP: "10.1.0.2", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	// One of everything that references the victim.
	if err := st.UpsertInterface(Interface{DeviceID: victim, IfIndex: 1, Name: "eth0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDeviceMonitor(DeviceMonitor{DeviceID: victim, Kind: "ping", Name: "Ping"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvent(&victim, "down", "Victim is down"); err != nil {
		t.Fatal(err)
	}
	mapID, err := st.UpsertMap(Map{SourceMapID: 42, Name: "Site map", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceMapObjects(mapID, []MapObject{
		{MapID: mapID, SourceAnnotationID: 1, Type: "Device", DeviceID: &victim, X: 10, Y: 10},
		{MapID: mapID, SourceAnnotationID: 2, Type: "Device", DeviceID: &keeper, X: 90, Y: 10},
	}); err != nil {
		t.Fatal(err)
	}
	tid, err := st.CreateTopology("Site topology", "manual")
	if err != nil {
		t.Fatal(err)
	}
	nv, err := st.AddTopologyNode(TopologyNode{TopologyID: tid, Label: "Victim", DeviceID: &victim})
	if err != nil {
		t.Fatal(err)
	}
	nk, err := st.AddTopologyNode(TopologyNode{TopologyID: tid, Label: "Keeper", DeviceID: &keeper})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddTopologyLink(TopologyLink{TopologyID: tid, FromNode: nv, ToNode: nk}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteDevice(victim); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDevice(victim); err == nil {
		t.Error("the device row itself survived DeleteDevice")
	}
	counts := map[string]string{
		"interfaces":      `SELECT COUNT(*) FROM interfaces WHERE device_id=?`,
		"samples":         `SELECT COUNT(*) FROM samples WHERE device_id=?`,
		"device_monitors": `SELECT COUNT(*) FROM device_monitors WHERE device_id=?`,
		"events":          `SELECT COUNT(*) FROM events WHERE device_id=?`,
		"map_objects":     `SELECT COUNT(*) FROM map_objects WHERE device_id=?`,
		"topology_nodes":  `SELECT COUNT(*) FROM topology_nodes WHERE device_id=?`,
		"topology_links":  `SELECT COUNT(*) FROM topology_links WHERE from_node=? OR to_node=?`,
	}
	for name, q := range counts {
		var n int
		args := []any{victim}
		if name == "topology_links" {
			args = []any{nv, nv}
		}
		if err := st.DB.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n != 0 {
			t.Errorf("%s: %d row(s) still reference the deleted device", name, n)
		}
	}
	// The keeper and its own rows are untouched.
	if _, err := st.GetDevice(keeper); err != nil {
		t.Error("an unrelated device was deleted")
	}
	objs, err := st.ListMapObjects(mapID)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Errorf("map objects after delete = %d, want 1 (the keeper's marker)", len(objs))
	}
	nodes, err := st.ListTopologyNodes(tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("topology nodes after delete = %d, want 1 (the keeper)", len(nodes))
	}
}

func TestGroupStatusSummary(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()

	root, err := st.AddGroup("Root", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.AddGroup("Child", &root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(Device{Name: "A", IP: "10.2.0.1", GroupID: &child}); err != nil {
		t.Fatal(err)
	}
	b, err := st.AddDevice(Device{Name: "B", IP: "10.2.0.2", GroupID: &child})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE devices SET status='up' WHERE id=?`, b); err != nil {
		t.Fatal(err)
	}
	sum, err := st.GroupStatusSummary()
	if err != nil {
		t.Fatal(err)
	}
	// The child's aggregate includes its own devices only; the
	// root's aggregate rolls the child's devices up.
	if got := sum[child]; got.Total != 2 || got.Up != 1 || got.Down != 0 {
		t.Errorf("child summary = %+v, want total 2 up 1 down 0", got)
	}
	if got := sum[root]; got.Total != 2 || got.Up != 1 {
		t.Errorf("root summary = %+v, want total 2 up 1 (rolled up from the child)", got)
	}
	if st := sum[child].AggregateStatus(); st != "unknown" {
		t.Errorf("aggregate with one up and one unknown = %q, want unknown", st)
	}
	if _, err := st.DB.Exec(`UPDATE devices SET status='down' WHERE group_id=?`, child); err != nil {
		t.Fatal(err)
	}
	sum, _ = st.GroupStatusSummary()
	if st := sum[root].AggregateStatus(); st != "down" {
		t.Errorf("aggregate with downs under it = %q, want down", st)
	}
}
