package store

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// Automatic topologies (v3.6). Two topologies can be generated
// from the stored data — once when a legacy database is
// imported and once on the first start of this version for
// existing installations:
//
//   - a hub topology: every device of one configured group
//     linked to the network's ASR 9K core router. The source
//     maps store no lines for that group, so these are DESIGN
//     uplinks (the way the network is built), and the
//     topology's description and the links' origin say exactly
//     that — they are never presented as discovered links.
//   - a combined topology: the union of two configured maps'
//     drawn lines, resolved to devices exactly as Topology
//     From Map resolves them, with parallel lines preserved
//     (never pair-deduped).
//
// Which group and maps feed the builder is configuration, not
// code: the "autotopo.groups" setting (JSON, see
// autoTopoConfig) holds the installation's own group and map
// names. The setting ships EMPTY, so with no configuration
// the builder has nothing to build and stays silent — which
// also matches the paused state of the feature while the
// Topology section is hidden from the interface (v3.6.1).
// Titles and descriptions are built at runtime from the
// configured names.
//
// A user who deletes one of these topologies gets a tombstone
// in the settings store, so the deleted topology is not
// recreated on the next start; the Topology page's "Rebuild
// automatic topologies" action clears the tombstones and
// regenerates both.

const (
	autoTopoConfigKey = "autotopo.groups"
	hubLinkOrigin     = "auto-hub-uplink"
	autoTopoSource    = "auto"
	deletedKeyPrefx   = "auto_topology_deleted."
	builtSettingKey   = "auto_topologies_built"
)

// autoTopoConfig names the sources of the two automatic
// topologies. An empty field disables that topology.
type autoTopoConfig struct {
	HubGroup string `json:"hub_group"` // group whose devices uplink to the ASR 9K hub
	MapA     string `json:"map_a"`     // first source map of the combined topology
	MapB     string `json:"map_b"`     // second source map of the combined topology
}

// autoTopoConfig reads the builder configuration from the
// settings store. The second return value is false when the
// setting is unset, empty or unreadable.
func (s *Store) autoTopoConfig() (autoTopoConfig, bool) {
	raw, ok := s.GetSetting(autoTopoConfigKey)
	if !ok || strings.TrimSpace(raw) == "" {
		return autoTopoConfig{}, false
	}
	var cfg autoTopoConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return autoTopoConfig{}, false
	}
	return cfg, true
}

// Titles and descriptions are assembled from the configured
// source names at runtime — never from literals.
func hubTopologyName(cfg autoTopoConfig) string {
	return cfg.HubGroup + " — ASR 9K Hub"
}

func hubTopologyDesc(cfg autoTopoConfig) string {
	return "Automatically generated from the network design: every device in the " +
		cfg.HubGroup + " group uplinks to the ASR 9K core router. " +
		"These are design uplinks, not discovered links."
}

func combinedTopologyName(cfg autoTopoConfig) string {
	return cfg.MapA + " + " + cfg.MapB
}

func combinedTopologyDesc(cfg autoTopoConfig) string {
	return "Automatically generated from the imported maps " + cfg.MapA + " and " + cfg.MapB +
		": the drawn lines of both maps, resolved to the devices they connect. " +
		"Parallel lines are kept as separate links."
}

// AutoTopoReport says what one build pass did, for logs and tests.
type AutoTopoReport struct {
	HubBuilt       bool
	HubName        string // title of the hub topology (when built)
	HubNodes       int
	HubLinks       int
	CombinedBuilt  bool
	CombinedName   string // title of the combined topology (when built)
	CombinedNodes  int
	CombinedLinks  int
	CombinedLinesA int // resolved lines taken from the first configured map
	CombinedLinesB int // resolved lines taken from the second configured map
}

// FindASRHub picks the network's ASR 9K core router out of all
// devices: a device named exactly "ASR-9K" wins, then the
// shortest name starting with "ASR-9K" (ties broken by name).
// Returns nil when no device matches — the hub topology is
// then skipped silently.
func FindASRHub(devices []Device) *Device {
	for i := range devices {
		if devices[i].Name == "ASR-9K" {
			return &devices[i]
		}
	}
	var best *Device
	for i := range devices {
		name := devices[i].Name
		if !strings.HasPrefix(name, "ASR-9K") {
			continue
		}
		if best == nil || len(name) < len(best.Name) ||
			(len(name) == len(best.Name) && name < best.Name) {
			best = &devices[i]
		}
	}
	return best
}

// autoTopologyDeleted reports whether the user has deleted the
// named automatic topology (its tombstone is set).
func (s *Store) autoTopologyDeleted(name string) bool {
	v, ok := s.GetSetting(deletedKeyPrefx + name)
	return ok && v == "1"
}

// MarkAutoTopologyDeleted records the tombstone for an automatic
// topology the user just deleted, so it is not recreated.
func (s *Store) MarkAutoTopologyDeleted(name string) {
	cfg, ok := s.autoTopoConfig()
	if !ok {
		return
	}
	if cfg.HubGroup != "" && name == hubTopologyName(cfg) ||
		cfg.MapA != "" && cfg.MapB != "" && name == combinedTopologyName(cfg) {
		_ = s.SetSetting(deletedKeyPrefx+name, "1")
	}
}

func (s *Store) topologyByName(name string) (*Topology, bool) {
	tops, err := s.ListTopologies()
	if err != nil {
		return nil, false
	}
	for i := range tops {
		if tops[i].Name == name {
			return &tops[i], true
		}
	}
	return nil, false
}

// EnsureAutoTopologies builds whichever automatic topologies are
// missing (and not tombstoned) from the current data. It is
// called after a legacy import and once on the first start of
// v3.6.0; the builtSettingKey flag records that the first-start
// pass ran, but the per-topology existence + tombstone checks
// are what actually decide — so an import that arrives after an
// empty first start still gets its topologies. With no
// "autotopo.groups" configuration there is nothing to build
// and the pass is a no-op.
func (s *Store) EnsureAutoTopologies() (AutoTopoReport, error) {
	var rep AutoTopoReport
	cfg, configured := s.autoTopoConfig()
	if !configured {
		_ = s.SetSetting(builtSettingKey, "3.6.0")
		return rep, nil
	}
	if cfg.HubGroup != "" {
		name := hubTopologyName(cfg)
		if _, ok := s.topologyByName(name); !ok && !s.autoTopologyDeleted(name) {
			n, l, err := s.buildHubTopology(cfg)
			if err != nil {
				return rep, err
			}
			if n > 0 {
				rep.HubBuilt, rep.HubName, rep.HubNodes, rep.HubLinks = true, name, n, l
			}
		}
	}
	if cfg.MapA != "" && cfg.MapB != "" {
		name := combinedTopologyName(cfg)
		if _, ok := s.topologyByName(name); !ok && !s.autoTopologyDeleted(name) {
			res, err := s.buildCombinedTopology(cfg)
			if err != nil {
				return rep, err
			}
			if res != nil {
				rep.CombinedBuilt = true
				rep.CombinedName = name
				rep.CombinedNodes = res.nodes
				rep.CombinedLinks = res.links
				rep.CombinedLinesA = res.linesA
				rep.CombinedLinesB = res.linesB
			}
		}
	}
	_ = s.SetSetting(builtSettingKey, "3.6.0")
	return rep, nil
}

// RebuildAutoTopologies deletes the existing automatic topologies
// (if any), clears their tombstones, and builds them again from
// the current data — the Topology page's rebuild action.
func (s *Store) RebuildAutoTopologies() (AutoTopoReport, error) {
	if cfg, ok := s.autoTopoConfig(); ok {
		names := []string{}
		if cfg.HubGroup != "" {
			names = append(names, hubTopologyName(cfg))
		}
		if cfg.MapA != "" && cfg.MapB != "" {
			names = append(names, combinedTopologyName(cfg))
		}
		for _, name := range names {
			if t, ok := s.topologyByName(name); ok {
				if err := s.DeleteTopology(t.ID); err != nil {
					return AutoTopoReport{}, err
				}
			}
			_ = s.SetSetting(deletedKeyPrefx+name, "")
		}
	}
	return s.EnsureAutoTopologies()
}

// buildHubTopology creates the configured group's hub topology.
// Returns (0, 0, nil) when the group or the ASR hub is missing —
// the caller then simply skips it.
func (s *Store) buildHubTopology(cfg autoTopoConfig) (nodes, links int, err error) {
	groups, err := s.ListGroups()
	if err != nil {
		return 0, 0, err
	}
	var gid *int64
	for _, g := range groups {
		if g.Name == cfg.HubGroup {
			id := g.ID
			gid = &id
			break
		}
	}
	if gid == nil {
		return 0, 0, nil
	}
	devices, err := s.ListDevices()
	if err != nil {
		return 0, 0, err
	}
	hub := FindASRHub(devices)
	if hub == nil {
		return 0, 0, nil
	}
	var members []Device
	for _, d := range devices {
		if d.GroupID != nil && *d.GroupID == *gid && d.ID != hub.ID {
			members = append(members, d)
		}
	}
	if len(members) == 0 {
		return 0, 0, nil
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })

	tid, err := s.CreateTopology(hubTopologyName(cfg), autoTopoSource)
	if err != nil {
		return 0, 0, err
	}
	fail := func(err error) (int, int, error) {
		_ = s.DeleteTopology(tid)
		return 0, 0, err
	}
	if err := s.SetTopologyDescription(tid, hubTopologyDesc(cfg)); err != nil {
		return fail(err)
	}
	hubNode, err := s.AddTopologyNode(TopologyNode{
		TopologyID: tid, Label: hub.Name, IP: hub.IP, Type: hub.DeviceType,
		DeviceID: &hub.ID, X: 0, Y: 0,
	})
	if err != nil {
		return fail(err)
	}
	nodes++
	// Members on a grid around the hub: the grid has one more
	// slot than there are members, and the hub takes the centre
	// slot, so no member lands on top of it. (The page's Tidy
	// View re-arranges the nodes for display anyway; these stored
	// positions only need to be sane.)
	total := len(members) + 1
	cols := int(math.Ceil(math.Sqrt(float64(total))))
	rows := int(math.Ceil(float64(total) / float64(cols)))
	centre := (rows/2)*cols + cols/2
	slot := 0
	for _, d := range members {
		if slot == centre {
			slot++
		}
		x := float64(slot%cols-cols/2) * 300
		y := float64(slot/cols-rows/2) * 230
		slot++
		dev := d
		nid, err := s.AddTopologyNode(TopologyNode{
			TopologyID: tid, Label: dev.Name, IP: dev.IP, Type: dev.DeviceType,
			DeviceID: &dev.ID, X: x, Y: y,
		})
		if err != nil {
			return fail(err)
		}
		nodes++
		if _, err := s.AddTopologyLink(TopologyLink{
			TopologyID: tid, FromNode: hubNode, ToNode: nid, Origin: hubLinkOrigin,
		}); err != nil {
			return fail(err)
		}
		links++
	}
	return nodes, links, nil
}

type combinedResult struct {
	nodes, links, linesA, linesB int
}

// buildCombinedTopology creates the combined topology from the
// two configured maps' resolved lines. Returns nil (and no
// error) when either map is missing or neither map yields a
// resolved line.
func (s *Store) buildCombinedTopology(cfg autoTopoConfig) (*combinedResult, error) {
	maps, err := s.ListMaps()
	if err != nil {
		return nil, err
	}
	byName := map[string]*Map{}
	for i := range maps {
		byName[maps[i].Name] = &maps[i]
	}
	a, okA := byName[cfg.MapA]
	b, okB := byName[cfg.MapB]
	if !okA || !okB {
		return nil, nil
	}
	type mapPart struct {
		m      *Map
		objs   []MapObject
		res    map[int64]ResolvedLine
		links  int
		shiftX float64
		maxY   float64
	}
	parts := []*mapPart{}
	shift := 0.0
	for _, m := range []*Map{a, b} {
		objs, err := s.ListMapObjects(m.ID)
		if err != nil {
			return nil, err
		}
		res := ResolveMapLines(objs)
		links := 0
		for _, o := range objs {
			if o.Type != "Line" {
				continue
			}
			if r, ok := res[o.ID]; ok && r.FromObject != 0 && r.ToObject != 0 && r.FromObject != r.ToObject {
				links++
			}
		}
		parts = append(parts, &mapPart{m: m, objs: objs, res: res, links: links, shiftX: shift, maxY: m.MaxY})
		// The next map starts to the right of this one's extent.
		shift += (m.MaxX - m.MinX) + 240
	}
	if parts[0].links+parts[1].links == 0 {
		return nil, nil
	}
	tid, err := s.CreateTopology(combinedTopologyName(cfg), autoTopoSource)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*combinedResult, error) {
		_ = s.DeleteTopology(tid)
		return nil, err
	}
	if err := s.SetTopologyDescription(tid, combinedTopologyDesc(cfg)); err != nil {
		return fail(err)
	}
	out := &combinedResult{linesA: parts[0].links, linesB: parts[1].links}
	for _, part := range parts {
		nodeByObject := map[int64]int64{}
		for _, o := range part.objs {
			if o.Type != "Device" {
				continue
			}
			// Only markers that take part in a resolved line
			// become nodes — the topology is the union of the
			// maps' lines, not of their whole device sets.
			used := false
			for _, r := range part.res {
				if r.FromObject == o.ID || r.ToObject == o.ID {
					used = true
					break
				}
			}
			if !used {
				continue
			}
			label := o.DeviceName
			if label == "" {
				label = "Device"
			}
			n := TopologyNode{
				TopologyID: tid, Label: label, IP: o.DeviceIP, Type: o.DeviceType,
				X: o.X + part.shiftX, Y: part.maxY - o.Y, // editor Y grows downward
			}
			if o.DeviceID != nil {
				n.DeviceID = o.DeviceID
			}
			nid, err := s.AddTopologyNode(n)
			if err != nil {
				return fail(err)
			}
			nodeByObject[o.ID] = nid
			out.nodes++
		}
		// Every resolved line becomes a link — parallel lines
		// between the same pair stay separate links.
		for _, o := range part.objs {
			if o.Type != "Line" {
				continue
			}
			r, ok := part.res[o.ID]
			if !ok || r.FromObject == 0 || r.ToObject == 0 {
				continue
			}
			from, to := nodeByObject[r.FromObject], nodeByObject[r.ToObject]
			if from == 0 || to == 0 || from == to {
				continue
			}
			if _, err := s.AddTopologyLink(TopologyLink{
				TopologyID: tid, FromNode: from, ToNode: to, Origin: "imported-map",
			}); err != nil {
				return fail(err)
			}
			out.links++
		}
	}
	return out, nil
}
