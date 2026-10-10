package store

import (
	"path/filepath"
	"testing"
)

// v3.6 automatic topologies: the ASR hub rule, the hub topology's
// shape (hub + one design uplink per group device), the combined
// two-map topology with parallel lines preserved, and the
// delete tombstone + rebuild behaviour. All group, map and
// device names are synthetic and reach the builder through the
// same "autotopo.groups" configuration a real installation
// would use.

const (
	testHubTitle      = "DISTRICT-A — ASR 9K Hub"
	testCombinedTitle = "CORE-A + CAMPUS-A"
	testAutoTopoCfg   = `{"hub_group":"DISTRICT-A","map_a":"CORE-A","map_b":"CAMPUS-A"}`
)

func TestFindASRHub(t *testing.T) {
	mk := func(names ...string) []Device {
		var out []Device
		for i, n := range names {
			out = append(out, Device{ID: int64(i + 1), Name: n})
		}
		return out
	}
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"RTR-1K", "ASR-9K-B", "RTR1K BB TO L3"}, "ASR-9K-B"},
		{[]string{"ASR-9K-LONG", "ASR-9K"}, "ASR-9K"},
		{[]string{"ASR-9K-LONGER", "ASR-9K-X"}, "ASR-9K-X"},
		{[]string{"ASR-9K-BB", "ASR-9K-AA"}, "ASR-9K-AA"},
		{[]string{"RTR-1K", "MX104"}, ""},
	}
	for _, c := range cases {
		got := FindASRHub(mk(c.names...))
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != c.want {
			t.Errorf("FindASRHub(%v) = %q, want %q", c.names, name, c.want)
		}
	}
}

func seedAutoTopoFixture(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })

	// The builder's sources come from configuration, exactly as
	// in a real installation.
	if err := st.SetSetting(autoTopoConfigKey, testAutoTopoCfg); err != nil {
		t.Fatal(err)
	}

	core, err := st.AddGroup("CORE-A", nil)
	if err != nil {
		t.Fatal(err)
	}
	dist, err := st.AddGroup("DISTRICT-A", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(Device{Name: "RTR-1K", IP: "192.0.2.1", GroupID: &core}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(Device{Name: "ASR-9K-B", IP: "192.0.2.2", GroupID: &core}); err != nil {
		t.Fatal(err)
	}
	for i, nm := range []string{"DIST-A", "DIST-B", "DIST-C"} {
		ip := "192.0.2.1" + string(rune('1'+i))
		if _, err := st.AddDevice(Device{Name: nm, IP: ip, GroupID: &dist}); err != nil {
			t.Fatal(err)
		}
	}

	// Two maps with drawn lines: CORE-A gets one line between
	// two markers; CAMPUS-A gets TWO parallel lines between the
	// same pair (parallel physical paths must survive as
	// separate links).
	devAt := map[string]int64{}
	addDev := func(name, ip string, gid int64) int64 {
		id, err := st.AddDevice(Device{Name: name, IP: ip, GroupID: &gid})
		if err != nil {
			t.Fatal(err)
		}
		devAt[name] = id
		return id
	}
	a1 := addDev("CORE-DEV-A", "192.0.2.21", core)
	a2 := addDev("CORE-DEV-B", "192.0.2.22", core)
	b1 := addDev("CAMP-DEV-A", "192.0.2.31", core)
	b2 := addDev("CAMP-DEV-B", "192.0.2.32", core)

	mkMap := func(srcID int64, name string, gid int64, objs []MapObject) {
		mid, err := st.UpsertMap(Map{SourceMapID: srcID, Name: name, GroupID: &gid, MinX: 0, MinY: 0, MaxX: 400, MaxY: 300})
		if err != nil {
			t.Fatal(err)
		}
		for i := range objs {
			objs[i].MapID = mid
		}
		if err := st.ReplaceMapObjects(mid, objs); err != nil {
			t.Fatal(err)
		}
	}
	dev := func(sa int64, devID int64, x, y float64) MapObject {
		return MapObject{SourceAnnotationID: sa, Type: "Device", DeviceID: &devID, X: x, Y: y, W: 32, H: 32}
	}
	line := func(sa int64, x, y, x2, y2 float64) MapObject {
		return MapObject{SourceAnnotationID: sa, Type: "Line", X: x, Y: y, X2: x2, Y2: y2}
	}
	_ = a1
	_ = a2
	_ = b1
	_ = b2
	mkMap(101, "CORE-A", core, []MapObject{
		dev(1, devAt["CORE-DEV-A"], 100, 100), dev(2, devAt["CORE-DEV-B"], 300, 100),
		line(3, 100, 100, 300, 100),
	})
	mkMap(102, "CAMPUS-A", core, []MapObject{
		dev(1, devAt["CAMP-DEV-A"], 100, 100), dev(2, devAt["CAMP-DEV-B"], 300, 100),
		line(3, 100, 100, 300, 100),
		line(4, 100, 108, 300, 108), // the parallel link
	})
	return st
}

func TestEnsureAutoTopologies(t *testing.T) {
	st := seedAutoTopoFixture(t)
	rep, err := st.EnsureAutoTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HubBuilt || rep.HubNodes != 4 || rep.HubLinks != 3 {
		t.Errorf("hub report = %+v, want built with 4 nodes (hub + 3 districts) and 3 links", rep)
	}
	if rep.HubName != testHubTitle {
		t.Errorf("hub name = %q, want %q (built from the configured group name)", rep.HubName, testHubTitle)
	}
	if !rep.CombinedBuilt || rep.CombinedNodes != 4 || rep.CombinedLinks != 3 {
		t.Errorf("combined report = %+v, want built with 4 nodes and 3 links (1 + 2 parallel)", rep)
	}
	if rep.CombinedName != testCombinedTitle {
		t.Errorf("combined name = %q, want %q (built from the configured map names)", rep.CombinedName, testCombinedTitle)
	}
	if rep.CombinedLinesA != 1 || rep.CombinedLinesB != 2 {
		t.Errorf("combined lines = %d + %d, want 1 + 2", rep.CombinedLinesA, rep.CombinedLinesB)
	}

	hub, ok := st.topologyByName(testHubTitle)
	if !ok {
		t.Fatal("hub topology missing")
	}
	if want := hubTopologyDesc(autoTopoConfig{HubGroup: "DISTRICT-A"}); hub.Description != want {
		t.Errorf("hub description = %q, want %q", hub.Description, want)
	}
	links, err := st.ListTopologyLinks(hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.Origin != "auto-hub-uplink" {
			t.Errorf("hub link origin = %q, want auto-hub-uplink", l.Origin)
		}
	}
	comb, ok := st.topologyByName(testCombinedTitle)
	if !ok {
		t.Fatal("combined topology missing")
	}
	clinks, err := st.ListTopologyLinks(comb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(clinks) != 3 {
		t.Fatalf("combined links = %d, want 3 (parallel lines preserved)", len(clinks))
	}
	// The parallel pair: two links share the same node pair.
	pairs := map[[2]int64]int{}
	for _, l := range clinks {
		k := [2]int64{l.FromNode, l.ToNode}
		if k[0] > k[1] {
			k[0], k[1] = k[1], k[0]
		}
		pairs[k]++
		if l.Origin != "imported-map" {
			t.Errorf("combined link origin = %q, want imported-map", l.Origin)
		}
	}
	maxPair := 0
	for _, n := range pairs {
		if n > maxPair {
			maxPair = n
		}
	}
	if maxPair != 2 {
		t.Errorf("no parallel pair survived: pair multiplicities %v", pairs)
	}

	// A second ensure pass changes nothing (no duplicates).
	rep2, err := st.EnsureAutoTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if rep2.HubBuilt || rep2.CombinedBuilt {
		t.Errorf("second EnsureAutoTopologies rebuilt: %+v", rep2)
	}
	tops, err := st.ListTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if len(tops) != 2 {
		t.Errorf("topologies after double ensure = %d, want 2", len(tops))
	}

	// Deleting the hub topology tombstones it: ensure does not
	// recreate it, but the rebuild action does.
	if err := st.DeleteTopology(hub.ID); err != nil {
		t.Fatal(err)
	}
	st.MarkAutoTopologyDeleted(testHubTitle)
	rep3, err := st.EnsureAutoTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if rep3.HubBuilt {
		t.Error("a user-deleted hub topology was recreated by Ensure")
	}
	rep4, err := st.RebuildAutoTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if !rep4.HubBuilt || rep4.HubNodes != 4 || rep4.HubLinks != 3 {
		t.Errorf("rebuild report = %+v, want the hub topology back at 4 nodes / 3 links", rep4)
	}
}

func TestEnsureAutoTopologiesSkipsSilently(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	// No configuration and no data: nothing is built, no error.
	rep, err := st.EnsureAutoTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if rep.HubBuilt || rep.CombinedBuilt {
		t.Errorf("unconfigured database produced topologies: %+v", rep)
	}
}

func TestEnsureAutoTopologiesUnconfigured(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	// Data exists but the "autotopo.groups" setting was never
	// filled in: the dormant builder has nothing to build.
	gid, err := st.AddGroup("DISTRICT-A", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(Device{Name: "ASR-9K-B", IP: "192.0.2.2", GroupID: &gid}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(Device{Name: "DIST-A", IP: "192.0.2.11", GroupID: &gid}); err != nil {
		t.Fatal(err)
	}
	rep, err := st.EnsureAutoTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if rep.HubBuilt || rep.CombinedBuilt {
		t.Errorf("builder ran without configuration: %+v", rep)
	}
	tops, err := st.ListTopologies()
	if err != nil {
		t.Fatal(err)
	}
	if len(tops) != 0 {
		t.Errorf("topologies without configuration = %d, want 0", len(tops))
	}
}
