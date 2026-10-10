package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"netgold/internal/store"
	"netgold/internal/topoxl"
)

func pathInt64(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

func (s *Server) listTopologies(w http.ResponseWriter, r *http.Request) {
	tops, err := s.Store.ListTopologies()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if tops == nil {
		tops = []store.Topology{}
	}
	writeJSON(w, 200, tops)
}

func (s *Server) createTopology(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		errJSON(w, 400, "name is required")
		return
	}
	source := req.Source
	if source != "excel" && source != "map" {
		source = "manual"
	}
	id, err := s.Store.CreateTopology(req.Name, source)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) topologyDetail(w http.ResponseWriter, id int64) {
	t, err := s.Store.GetTopology(id)
	if err != nil {
		errJSON(w, 404, "topology not found")
		return
	}
	nodes, err := s.Store.ListTopologyNodes(id)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	links, err := s.Store.ListTopologyLinks(id)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if nodes == nil {
		nodes = []store.TopologyNode{}
	}
	if links == nil {
		links = []store.TopologyLink{}
	}
	writeJSON(w, 200, map[string]any{"topology": t, "nodes": nodes, "links": links})
}

func (s *Server) getTopology(w http.ResponseWriter, r *http.Request) {
	s.topologyDetail(w, pathID(r))
}

func (s *Server) renameTopology(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		errJSON(w, 400, "name is required")
		return
	}
	if err := s.Store.RenameTopology(pathID(r), req.Name); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteTopology(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	// Deleting an automatic topology is a deliberate act: record
	// its tombstone so the first-start/import builder does not
	// quietly recreate it (v3.6). The Topology page's rebuild
	// action clears tombstones again.
	if t, err := s.Store.GetTopology(id); err == nil {
		s.Store.MarkAutoTopologyDeleted(t.Name)
	}
	if err := s.Store.DeleteTopology(id); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// rebuildAutoTopologies regenerates the two automatic topologies
// (v3.6) from the current data — the hub topology and the combined
// two-map topology — replacing any existing copies and clearing
// their deletion tombstones.
func (s *Server) rebuildAutoTopologies(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Store.RebuildAutoTopologies()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"hub_built": rep.HubBuilt, "hub_nodes": rep.HubNodes, "hub_links": rep.HubLinks,
		"combined_built": rep.CombinedBuilt, "combined_nodes": rep.CombinedNodes, "combined_links": rep.CombinedLinks,
	})
}

func (s *Server) addTopologyNode(w http.ResponseWriter, r *http.Request) {
	tid := pathID(r)
	var n store.TopologyNode
	if !decode(w, r, &n) {
		return
	}
	n.TopologyID = tid
	if strings.TrimSpace(n.Label) == "" {
		errJSON(w, 400, "node label is required")
		return
	}
	if n.DeviceID == nil && n.IP != "" {
		if d, err := s.Store.DeviceByIP(n.IP); err == nil {
			n.DeviceID = &d.ID
		}
	}
	id, err := s.Store.AddTopologyNode(n)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) updateTopologyNode(w http.ResponseWriter, r *http.Request) {
	tid, nid := pathID(r), pathInt64(r, "nid")
	existing, err := s.Store.GetTopologyNode(tid, nid)
	if err != nil {
		errJSON(w, 404, "node not found")
		return
	}
	var n store.TopologyNode
	if !decode(w, r, &n) {
		return
	}
	n.ID, n.TopologyID = nid, tid
	// The editor always sends the complete node; an empty label falls
	// back to the stored one so a position-only save cannot blank it.
	if n.Label == "" {
		n.Label = existing.Label
	}
	if n.DeviceID == nil && n.IP != "" {
		if d, err := s.Store.DeviceByIP(n.IP); err == nil {
			n.DeviceID = &d.ID
		}
	}
	if err := s.Store.UpdateTopologyNode(n); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteTopologyNode(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteTopologyNode(pathID(r), pathInt64(r, "nid")); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) addTopologyLink(w http.ResponseWriter, r *http.Request) {
	tid := pathID(r)
	var l store.TopologyLink
	if !decode(w, r, &l) {
		return
	}
	l.TopologyID = tid
	if l.FromNode == 0 || l.ToNode == 0 || l.FromNode == l.ToNode {
		errJSON(w, 400, "a link needs two different nodes")
		return
	}
	if _, err := s.Store.GetTopologyNode(tid, l.FromNode); err != nil {
		errJSON(w, 400, "from_node does not belong to this topology")
		return
	}
	if _, err := s.Store.GetTopologyNode(tid, l.ToNode); err != nil {
		errJSON(w, 400, "to_node does not belong to this topology")
		return
	}
	id, err := s.Store.AddTopologyLink(l)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) deleteTopologyLink(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteTopologyLink(pathID(r), pathInt64(r, "lid")); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// topologyFromMap copies an imported legacy map into a new editable
// topology: nodes with their positions (converted to the editor's
// coordinate system) AND links reconstructed from the map's drawn
// lines (v3.1). the legacy system stores its map lines as pure geometry —
// its database records no port-to-port links — so each line's
// endpoints are matched geometrically to the nearest device icons
// (store.ResolveMapLines). Reconstructed links are stored with
// origin "imported-map" and the UI captions them as reconstructed; nothing
// claims they are measured port-to-port links.
func (s *Server) topologyFromMap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MapID int64  `json:"map_id"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, err := s.Store.GetMap(req.MapID)
	if err != nil {
		errJSON(w, 404, "map not found")
		return
	}
	objs, err := s.Store.ListMapObjects(m.ID)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	name := req.Name
	if name == "" {
		name = m.Name + " (topology)"
	}
	tid, err := s.Store.CreateTopology(name, "map")
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	fail := func(err error) {
		_ = s.Store.DeleteTopology(tid)
		errJSON(w, 500, err.Error())
	}
	copied := 0
	nodeByObject := map[int64]int64{} // map object id -> topology node id
	for _, o := range objs {
		if o.Type != "Device" {
			continue
		}
		label := o.DeviceName
		if label == "" {
			label = fmt.Sprintf("Device %d", o.SourceDeviceID)
		}
		n := store.TopologyNode{
			TopologyID: tid, Label: label, IP: o.DeviceIP, Type: o.DeviceType,
			X: o.X, Y: m.MaxY - o.Y, // map Y grows upward; the editor's grows downward
		}
		if o.DeviceID != nil {
			n.DeviceID = o.DeviceID
		}
		nid, err := s.Store.AddTopologyNode(n)
		if err != nil {
			fail(err)
			return
		}
		nodeByObject[o.ID] = nid
		copied++
	}
	// Links: resolve the map's drawn lines against its device markers
	// and copy EVERY resolved line as its own link (v3.6) — parallel
	// drawn lines between the same two icons are distinct links in
	// the source network (separate physical paths), so they are
	// never merged into one. Resolution is per marker, not per
	// device: two markers of one merged device are two nodes, and a
	// line drawn between them is a real link.
	resolved := store.ResolveMapLines(objs)
	linksCopied, linesResolved := 0, 0
	for _, o := range objs {
		if o.Type != "Line" {
			continue
		}
		r, ok := resolved[o.ID]
		if !ok || r.FromObject == 0 || r.ToObject == 0 {
			continue
		}
		from, to := nodeByObject[r.FromObject], nodeByObject[r.ToObject]
		if from == 0 || to == 0 || from == to {
			continue
		}
		linesResolved++
		if _, err := s.Store.AddTopologyLink(store.TopologyLink{
			TopologyID: tid, FromNode: from, ToNode: to, Origin: "imported-map",
		}); err != nil {
			fail(err)
			return
		}
		linksCopied++
	}
	writeJSON(w, 201, map[string]any{
		"id": tid, "nodes_copied": copied, "links_copied": linksCopied,
		"note": fmt.Sprintf("Nodes were copied with their map positions. %d link(s) were reconstructed "+
			"from the lines drawn on the legacy system map (%d drawn line(s) resolved to a pair of device icons): "+
			"each line's endpoints were matched to the nearest device icons. The source database records "+
			"no port-to-port links, so treat these as the map's drawn connections and adjust them in the editor if needed.",
			linksCopied, linesResolved),
	})
}

// topologyImport accepts a multipart .xlsx upload in the template format.
func (s *Server) topologyImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		errJSON(w, 400, "bad upload: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		errJSON(w, 400, "no file uploaded (form field \"file\")")
		return
	}
	defer file.Close()
	p, err := topoxl.Parse(file)
	if err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	name := r.FormValue("name")
	if name == "" {
		name = "Imported topology"
	}
	tid, err := topoxl.Import(s.Store, name, "excel", p)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": tid, "nodes": len(p.Nodes), "links": len(p.Links)})
}

func (s *Server) topologyExport(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	t, err := s.Store.GetTopology(id)
	if err != nil {
		errJSON(w, 404, "topology not found")
		return
	}
	data, err := topoxl.Export(s.Store, id)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	name := strings.Map(func(c rune) rune {
		if c == '"' || c == '/' || c == '\\' {
			return '_'
		}
		return c
	}, t.Name)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+".xlsx\"")
	w.Write(data)
}

func (s *Server) topologyTemplate(w http.ResponseWriter, r *http.Request) {
	data, err := topoxl.Template()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\"netgold-topology-template.xlsx\"")
	w.Write(data)
}
