package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.DB.Close() })
	return st
}

func TestTopologyCRUD(t *testing.T) {
	st := openTestStore(t)

	devID, err := st.AddDevice(Device{Name: "Core Router", IP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("add device: %v", err)
	}
	d, _ := st.GetDevice(devID)
	_, _ = st.TransitionDeviceStatus(d, "up", "up", "up")

	tid, err := st.CreateTopology("HQ Network", "manual")
	if err != nil {
		t.Fatalf("create topology: %v", err)
	}
	n1, err := st.AddTopologyNode(TopologyNode{TopologyID: tid, Label: "Core Router", IP: "10.0.0.1", Type: "router", X: 100, Y: 80})
	if err != nil {
		t.Fatalf("add node 1: %v", err)
	}
	// Node 2 links to the device by id; node 1 links by IP match.
	n2, err := st.AddTopologyNode(TopologyNode{TopologyID: tid, Label: "Core (by id)", DeviceID: &devID, X: 300, Y: 80})
	if err != nil {
		t.Fatalf("add node 2: %v", err)
	}
	lid, err := st.AddTopologyLink(TopologyLink{TopologyID: tid, FromNode: n1, ToNode: n2, Label: "uplink", Bandwidth: "1 Gbps"})
	if err != nil {
		t.Fatalf("add link: %v", err)
	}

	top, err := st.GetTopology(tid)
	if err != nil || top == nil {
		t.Fatalf("get topology: %v", err)
	}
	if top.Nodes != 2 || top.Links != 1 || top.Source != "manual" || top.Name != "HQ Network" {
		t.Fatalf("topology summary wrong: %+v", top)
	}
	nodes, err := st.ListTopologyNodes(tid)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("list nodes: %v len=%d", err, len(nodes))
	}
	for _, n := range nodes {
		if n.DeviceStatus != "up" {
			t.Errorf("node %q: device status = %q, want up (linked device)", n.Label, n.DeviceStatus)
		}
	}
	links, err := st.ListTopologyLinks(tid)
	if err != nil || len(links) != 1 || links[0].ID != lid || links[0].Bandwidth != "1 Gbps" {
		t.Fatalf("list links: %v %+v", err, links)
	}

	// Update (drag-save path).
	gn, _ := st.GetTopologyNode(tid, n1)
	gn.X, gn.Y = 111, 222
	if err := st.UpdateTopologyNode(*gn); err != nil {
		t.Fatalf("update node: %v", err)
	}
	gn2, _ := st.GetTopologyNode(tid, n1)
	if gn2.X != 111 || gn2.Y != 222 {
		t.Fatalf("node position not saved: %+v", gn2)
	}

	// Rename.
	if err := st.RenameTopology(tid, "HQ Core"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	top, _ = st.GetTopology(tid)
	if top.Name != "HQ Core" {
		t.Fatalf("rename not applied: %q", top.Name)
	}

	// Deleting a node removes its links.
	if err := st.DeleteTopologyNode(tid, n1); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	top, _ = st.GetTopology(tid)
	if top.Nodes != 1 || top.Links != 0 {
		t.Fatalf("after node delete: nodes=%d links=%d, want 1/0", top.Nodes, top.Links)
	}

	// Delete topology removes everything.
	if err := st.DeleteTopology(tid); err != nil {
		t.Fatalf("delete topology: %v", err)
	}
	if _, err := st.GetTopology(tid); err == nil {
		t.Fatal("deleted topology still readable")
	}
	list, _ := st.ListTopologies()
	if len(list) != 0 {
		t.Fatalf("list after delete: %d topologies", len(list))
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	if _, ok := st.GetSetting("email.smtp_host"); ok {
		t.Fatal("unexpected setting present in a fresh database")
	}
	if err := st.SetSetting("email.smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("email.smtp_host", "mail.example.com"); err != nil {
		t.Fatal(err)
	}
	v, ok := st.GetSetting("email.smtp_host")
	if !ok || v != "mail.example.com" {
		t.Fatalf("setting = %q ok=%v", v, ok)
	}
	m, err := st.GetSettings("email.smtp_host", "email.missing")
	if err != nil || m["email.smtp_host"] != "mail.example.com" {
		t.Fatalf("GetSettings: %v %+v", err, m)
	}
	if _, present := m["email.missing"]; present {
		t.Fatal("missing key should be absent from GetSettings result")
	}
}

func TestSNMPv3DeviceFieldsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	id, err := st.AddDevice(Device{
		Name: "V3 Switch", IP: "10.0.0.9",
		SNMPVersion: "v3", V3SecName: "nocuser", V3AuthProto: "SHA", V3AuthPass: "auth-secret",
		V3PrivProto: "AES", V3PrivPass: "priv-secret", V3SecLevel: "authPriv",
	})
	if err != nil {
		t.Fatalf("add device: %v", err)
	}
	d, err := st.GetDevice(id)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.SNMPVersion != "v3" || d.V3SecName != "nocuser" || d.V3AuthProto != "SHA" ||
		d.V3AuthPass != "auth-secret" || d.V3PrivProto != "AES" || d.V3PrivPass != "priv-secret" ||
		d.V3SecLevel != "authPriv" {
		t.Fatalf("v3 fields not round-tripped: %+v", d)
	}
	// Update path.
	d.V3SecLevel = "authNoPriv"
	d.V3PrivProto, d.V3PrivPass = "", ""
	if err := st.UpdateDevice(*d); err != nil {
		t.Fatalf("update device: %v", err)
	}
	d2, _ := st.GetDevice(id)
	if d2.V3SecLevel != "authNoPriv" || d2.V3PrivPass != "" {
		t.Fatalf("update not applied: %+v", d2)
	}
}

func TestMonitorStateTransitionEvents(t *testing.T) {
	st := openTestStore(t)
	devID, _ := st.AddDevice(Device{Name: "Web Server", IP: "10.0.0.20"})
	if _, err := st.AddDeviceMonitor(DeviceMonitor{DeviceID: devID, Kind: "service", Name: "HTTP", Port: 80, IntervalSec: 60}); err != nil {
		t.Fatal(err)
	}
	mons, _ := st.ListDeviceMonitors(devID)
	if len(mons) != 1 || mons[0].IntervalSec != 60 {
		t.Fatalf("monitors: %+v", mons)
	}
	m := mons[0]
	// First result sets the state but writes no event (no previous state).
	tr, err := st.TransitionMonitorState(m, "down", "Monitor HTTP on Web Server (10.0.0.20) is down: connection refused")
	if err != nil || tr {
		t.Fatalf("first transition: tr=%v err=%v", tr, err)
	}
	// Same state again: no event.
	tr, _ = st.TransitionMonitorState(m, "down", "still down")
	if tr {
		t.Fatal("repeated down state wrote an event")
	}
	// Recovery: exactly one "monitor" event.
	tr, _ = st.TransitionMonitorState(m, "up", "Monitor HTTP on Web Server (10.0.0.20) is up")
	if !tr {
		t.Fatal("recovery did not write an event")
	}
	evs, _ := st.ListEvents(10)
	n := 0
	for _, e := range evs {
		if e.Kind == "monitor" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("monitor events = %d, want 1", n)
	}
	mons, _ = st.ListDeviceMonitors(devID)
	if mons[0].State != "up" || mons[0].LastCheck == 0 {
		t.Fatalf("monitor state not stored: %+v", mons[0])
	}
	svc, _ := st.ListServiceMonitors()
	if len(svc) != 1 {
		t.Fatalf("ListServiceMonitors = %d, want 1", len(svc))
	}
}
