package legacyimport

import (
	"path/filepath"
	"testing"

	"netgold/internal/store"
)

type fakeSource struct {
	sch  Schema
	data map[string][]map[string]any
}

func (f *fakeSource) Schema() (Schema, error) { return f.sch, nil }
func (f *fakeSource) Rows(t string) ([]map[string]any, error) {
	return f.data[t], nil
}
func (f *fakeSource) Close() error { return nil }

// fakeLegacy143 mimics the real the legacy system 14.3 schema/table names measured
// on a restored production database: IPs on NetworkInterface (not Device),
// membership in PivotDeviceToGroup, assignments in
// PivotActiveMonitorTypeToDevice, root group id 0 with NULL parent.
func fakeLegacy143() *fakeSource {
	tables := []string{
		"Device", "DeviceGroup", "NetworkInterface", "PivotDeviceToGroup",
		"DeviceType", "ActiveMonitorType", "PivotActiveMonitorTypeToDevice",
		"PivotCredentialTypeToDevice", "MapView", "Annotation",
	}
	sch := Schema{Tables: map[string][]string{}}
	for _, t := range tables {
		sch.Tables[t] = []string{"stub"}
	}
	return &fakeSource{
		sch: sch,
		data: map[string][]map[string]any{
			"DeviceGroup": {
				{"nDeviceGroupID": int64(0), "sGroupName": "ROOT-SITE", "nParentGroupID": nil},
				{"nDeviceGroupID": int64(35), "sGroupName": "CORE-SITE", "nParentGroupID": int64(0)},
			},
			"DeviceType": {
				{"nDeviceTypeID": int64(7), "sDisplayName": "Router"},
			},
			"Device": {
				{"nDeviceID": int64(10), "sDisplayName": "core-router", "nDeviceTypeID": int64(7), "bSnmpManageable": true, "bRemoved": false, "nDefaultNetworkInterfaceID": int64(100)},
				{"nDeviceID": int64(11), "sDisplayName": "branch-sw", "nDeviceTypeID": int64(7), "bSnmpManageable": false, "bRemoved": false, "nDefaultNetworkInterfaceID": int64(101)},
				{"nDeviceID": int64(12), "sDisplayName": "dead-device", "nDeviceTypeID": int64(7), "bSnmpManageable": false, "bRemoved": true, "nDefaultNetworkInterfaceID": int64(103)},
				{"nDeviceID": int64(13), "sDisplayName": "no-ip-device", "nDeviceTypeID": int64(7), "bSnmpManageable": false, "bRemoved": false, "nDefaultNetworkInterfaceID": int64(102)},
				// Twin: same IP as core-router (real legacy DBs do this — a
				// branch node and its switch share one address).
				{"nDeviceID": int64(14), "sDisplayName": "core-router-twin", "nDeviceTypeID": int64(7), "bSnmpManageable": false, "bRemoved": false, "nDefaultNetworkInterfaceID": int64(104)},
			},
			"NetworkInterface": {
				{"nNetworkInterfaceID": int64(100), "nDeviceID": int64(10), "sNetworkAddress": "10.1.1.1", "sNetworkName": "10.1.1.1"},
				{"nNetworkInterfaceID": int64(101), "nDeviceID": int64(11), "sNetworkAddress": "10.1.2.1", "sNetworkName": "branch"},
				{"nNetworkInterfaceID": int64(102), "nDeviceID": int64(13), "sNetworkAddress": "not-an-ip", "sNetworkName": ""},
				{"nNetworkInterfaceID": int64(103), "nDeviceID": int64(12), "sNetworkAddress": "10.1.3.1", "sNetworkName": ""},
				{"nNetworkInterfaceID": int64(104), "nDeviceID": int64(14), "sNetworkAddress": "10.1.1.1", "sNetworkName": "10.1.1.1"},
			},
			"PivotDeviceToGroup": {
				{"nDeviceID": int64(10), "nDeviceGroupID": int64(35)},
				{"nDeviceID": int64(11), "nDeviceGroupID": int64(35)},
				{"nDeviceID": int64(13), "nDeviceGroupID": int64(0)},
				{"nDeviceID": int64(14), "nDeviceGroupID": int64(0)},
			},
			"ActiveMonitorType": {
				{"nActiveMonitorTypeID": int64(1), "sMonitorTypeName": "Ping"},
				{"nActiveMonitorTypeID": int64(2), "sMonitorTypeName": "Telnet"},
				{"nActiveMonitorTypeID": int64(6), "sMonitorTypeName": "SNMP"},
			},
			"PivotActiveMonitorTypeToDevice": {
				{"nDeviceID": int64(10), "nActiveMonitorTypeID": int64(1), "bRemoved": false, "sArgument": ""},
				{"nDeviceID": int64(10), "nActiveMonitorTypeID": int64(6), "bRemoved": false, "sArgument": ""},
				{"nDeviceID": int64(11), "nActiveMonitorTypeID": int64(1), "bRemoved": false, "sArgument": ""},
				{"nDeviceID": int64(11), "nActiveMonitorTypeID": int64(2), "bRemoved": false, "sArgument": ""},
				{"nDeviceID": int64(10), "nActiveMonitorTypeID": int64(1), "bRemoved": true, "sArgument": ""},
				{"nDeviceID": int64(14), "nActiveMonitorTypeID": int64(2), "bRemoved": false, "sArgument": ""},
			},
			"PivotCredentialTypeToDevice": {
				{"nCredentialTypeID": int64(3), "nDeviceID": int64(10)},
				{"nCredentialTypeID": int64(4), "nDeviceID": int64(14)},
			},
			"MapView": {
				{"nMapViewID": int64(1), "nDeviceGroupID": int64(35), "nMapType": int64(0), "sDefinition": testMapXML},
			},
			"Annotation": {
				{"nAnnotationID": int64(201), "nMapViewID": int64(1), "nZorder": int64(51), "nDeviceID": int64(10), "sDefinition": testDeviceAnnXML},
				{"nAnnotationID": int64(202), "nMapViewID": int64(1), "nZorder": int64(70), "nDeviceID": nil, "sDefinition": testLineAnnXML},
				{"nAnnotationID": int64(203), "nMapViewID": int64(1), "nZorder": int64(8), "nDeviceID": nil, "sDefinition": testTextAnnXML},
			},
		},
	}
}

const testMapXML = `<?xml version="1.0"?>
<Map>
	<Properties><ShowGrid>true</ShowGrid></Properties>
	<Envelope>
		<Min><X>-514.27636719</X><Y>-304.00000000</Y></Min>
		<Max><X>465.37890625</X><Y>264.00000000</Y></Max>
	</Envelope>
</Map>`

const testDeviceAnnXML = `<?xml version="1.0"?>
<Device>
	<Object>Device</Object>
	<Envelope>
		<Primary><X>-77.31567383</X><Y>-9.43395996</Y></Primary>
		<Secondary><X>-2.24206543</X><Y>65.63964844</Y></Secondary>
	</Envelope>
	<Width>1</Width>
	<ZOrder>51</ZOrder>
	<LocationX>-39.77886963</LocationX>
	<LocationY>28.10284424</LocationY>
	<DimensionX>75.07360840</DimensionX>
	<DimensionY>75.07360840</DimensionY>
	<Monitors>
		<Monitor><nID>6</nID><Name>Ping</Name><Comment/><Links></Links></Monitor>
	</Monitors>
</Device>`

const testLineAnnXML = `<?xml version="1.0"?>
<Line>
	<Object>Line</Object>
	<Envelope>
		<Primary><X>124.42376709</X><Y>-98.83410645</Y></Primary>
		<Secondary><X>278.75170898</X><Y>-98.83410645</Y></Secondary>
	</Envelope>
	<Width>3</Width>
	<ZOrder>70</ZOrder>
	<StartX>278.75170898</StartX>
	<StartY>-98.83410645</StartY>
	<EndX>124.42376709</EndX>
	<EndY>-98.83410645</EndY>
</Line>`

const testTextAnnXML = `<?xml version="1.0"?>
<Text>
	<Object>Text</Object>
	<Envelope>
		<Primary><X>-335.64306641</X><Y>-268.01635742</Y></Primary>
		<Secondary><X>-272.35693359</X><Y>-243.98365742</Y></Secondary>
	</Envelope>
	<Width>1</Width>
	<ZOrder>8</ZOrder>
	<Label>CARRIER-A</Label>
</Text>`

func TestPreview143(t *testing.T) {
	p, err := PreviewImport(fakeLegacy143())
	if err != nil {
		t.Fatal(err)
	}
	// The v1 bug: MonitorTable was "MonitorState" with Monitors=8 on the
	// real DB. Here the pivot table must be picked and assignments counted.
	if p.MonitorTable != "PivotActiveMonitorTypeToDevice" {
		t.Fatalf("monitor table = %q", p.MonitorTable)
	}
	if p.Devices != 4 || p.Groups != 2 || p.Interfaces != 4 {
		t.Fatalf("preview counts = %+v", p)
	}
	if p.Monitors != 5 {
		t.Fatalf("monitors = %d (breakdown %v)", p.Monitors, p.MonitorBreakdown)
	}
	want := map[string]int{"Ping": 2, "Telnet": 2, "SNMP": 1}
	for k, v := range want {
		if p.MonitorBreakdown[k] != v {
			t.Fatalf("breakdown[%s] = %d, want %d (all: %v)", k, p.MonitorBreakdown[k], v, p.MonitorBreakdown)
		}
	}
	if p.Maps != 1 || p.MapObjects != 3 || p.CredentialDevices != 2 {
		t.Fatalf("maps/objects/creds = %d/%d/%d", p.Maps, p.MapObjects, p.CredentialDevices)
	}
}

func TestImport143(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	rep, err := RunImport(fakeLegacy143(), st)
	if err != nil {
		t.Fatal(err)
	}
	if rep.GroupsImported != 2 || rep.DevicesImported != 2 || rep.DevicesSkipped != 1 {
		t.Fatalf("groups/devices/skipped = %d/%d/%d (%+v)", rep.GroupsImported, rep.DevicesImported, rep.DevicesSkipped, rep)
	}
	if rep.DevicesMerged != 1 {
		t.Fatalf("devices merged = %d (twin should merge into core-router)", rep.DevicesMerged)
	}
	if rep.InterfacesImported != 2 {
		t.Fatalf("interfaces = %d", rep.InterfacesImported)
	}
	if rep.MonitorsFound != 5 || rep.MonitorsImported != 5 {
		t.Fatalf("monitors found/imported = %d/%d", rep.MonitorsFound, rep.MonitorsImported)
	}
	if rep.MapsImported != 1 || rep.MapObjectsImported != 3 || rep.MapObjectsSkipped != 0 {
		t.Fatalf("maps/objects = %d/%d skipped %d", rep.MapsImported, rep.MapObjectsImported, rep.MapObjectsSkipped)
	}
	if len(rep.NeedsCredentials) != 2 {
		t.Fatalf("needs creds = %v", rep.NeedsCredentials)
	}

	groups, _ := st.ListGroups()
	byName := map[string]store.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	root, ok := byName["ROOT-SITE"]
	if !ok || root.ParentID != nil {
		t.Fatalf("root group wrong: %+v", byName)
	}
	core, ok := byName["CORE-SITE"]
	if !ok || core.ParentID == nil || *core.ParentID != root.ID {
		t.Fatalf("child group parent wrong: %+v (root %+v)", core, root)
	}

	devs, _ := st.ListDevices()
	if len(devs) != 2 {
		t.Fatalf("devices in store = %d", len(devs))
	}
	byIP := map[string]store.Device{}
	for _, d := range devs {
		byIP[d.IP] = d
	}
	cr := byIP["10.1.1.1"]
	if cr.Name != "core-router" || cr.Type != "Router" || !cr.NeedsCredentials || cr.SnmpDisabled {
		t.Fatalf("core-router wrong: %+v", cr)
	}
	if cr.GroupID == nil || *cr.GroupID != core.ID {
		t.Fatalf("core-router group = %v, want CORE-SITE %d", cr.GroupID, core.ID)
	}
	br := byIP["10.1.2.1"]
	if !br.SnmpDisabled || br.NeedsCredentials {
		t.Fatalf("branch-sw flags wrong: %+v", br)
	}
	// Monitors: core-router Ping + SNMP(161) + Telnet(23, inherited from
	// its merged twin); branch-sw Ping + Telnet(23).
	cm, _ := st.ListDeviceMonitors(cr.ID)
	if len(cm) != 3 {
		t.Fatalf("core monitors = %+v", cm)
	}
	seen := map[string]store.DeviceMonitor{}
	for _, m := range cm {
		seen[m.Name] = m
	}
	if seen["Ping"].Kind != "ping" || seen["Ping"].Port != 0 {
		t.Fatalf("ping monitor = %+v", seen["Ping"])
	}
	if seen["SNMP"].Kind != "snmp" || seen["SNMP"].Port != 161 {
		t.Fatalf("snmp monitor = %+v", seen["SNMP"])
	}
	if seen["Telnet"].Kind != "service" || seen["Telnet"].Port != 23 {
		t.Fatalf("merged twin telnet monitor = %+v", seen["Telnet"])
	}
	if cr.Name != "core-router" {
		t.Fatalf("first device row should win the name, got %q", cr.Name)
	}
	bm, _ := st.ListDeviceMonitors(br.ID)
	if len(bm) != 2 {
		t.Fatalf("branch monitors = %+v", bm)
	}
	for _, m := range bm {
		if m.Name == "Telnet" && (m.Kind != "service" || m.Port != 23) {
			t.Fatalf("telnet monitor = %+v", m)
		}
	}
	// Interfaces landed.
	ifs, _ := st.ListInterfaces(cr.ID)
	if len(ifs) != 1 || ifs[0].IfIndex != 1 {
		t.Fatalf("core interfaces = %+v", ifs)
	}
	// Maps landed with positions.
	maps, _ := st.ListMaps()
	if len(maps) != 1 || maps[0].Name != "CORE-SITE" || maps[0].Objects != 3 {
		t.Fatalf("maps = %+v", maps)
	}
	objs, _ := st.ListMapObjects(maps[0].ID)
	if len(objs) != 3 {
		t.Fatalf("map objects = %d", len(objs))
	}
	for _, o := range objs {
		switch o.Type {
		case "Device":
			if o.DeviceID == nil || *o.DeviceID != cr.ID {
				t.Fatalf("device object not linked: %+v", o)
			}
			if o.X != -39.77886963 || o.Y != 28.10284424 {
				t.Fatalf("device object position = %v,%v", o.X, o.Y)
			}
		case "Line":
			if o.X != 278.75170898 || o.X2 != 124.42376709 {
				t.Fatalf("line endpoints = %v -> %v", o.X, o.X2)
			}
		case "Text":
			if o.Text != "CARRIER-A" {
				t.Fatalf("text object = %+v", o)
			}
		}
	}
}

func TestMapMonitor(t *testing.T) {
	cases := []struct {
		name, arg string
		kind      string
		port      int
	}{
		{"Ping", "", "ping", 0},
		{"SNMP", "", "snmp", 161},
		{"Telnet", "", "service", 23},
		{"FTP", "", "service", 21},
		{"HTTP", "", "service", 80},
		{"HTTP", "8080", "service", 8080}, // instance argument wins
		{"Custom TCP", "", "service", 0},
	}
	for _, c := range cases {
		kind, port := MapMonitor(c.name, c.arg)
		if kind != c.kind || port != c.port {
			t.Fatalf("MapMonitor(%q,%q) = %s/%d, want %s/%d", c.name, c.arg, kind, port, c.kind, c.port)
		}
	}
}

func TestMapDeviceRow(t *testing.T) {
	if _, ok := MapDeviceRow(map[string]any{"sDisplayName": "x", "bRemoved": true}, "10.0.0.1"); ok {
		t.Fatal("removed device should not map")
	}
	if _, ok := MapDeviceRow(map[string]any{"sDisplayName": "x"}, "not-an-ip"); ok {
		t.Fatal("bad IP should not map")
	}
	d, ok := MapDeviceRow(map[string]any{"nDeviceID": int64(5), "sDisplayName": "r1", "bSnmpManageable": true}, "10.0.0.5")
	if !ok || d.Name != "r1" || !d.SnmpManageable || d.OldID != 5 {
		t.Fatalf("d=%+v ok=%v", d, ok)
	}
}

func TestDSNDefaultDB(t *testing.T) {
	d := ConnConfig{Server: "SRV", Instance: "LEGACY", User: "sa", Password: "x"}.DSN()
	if d == "" {
		t.Fatal("empty dsn")
	}
}
