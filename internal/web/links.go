package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"

	"netgold/internal/linkdisc"
	"netgold/internal/store"
)

// Discovered-links endpoints. A run is started explicitly (or from the
// Topology page); it walks every SNMP device's LLDP/CDP/FDB/ARP tables
// and correlates the results into stored links. The last report is kept
// in memory for the status endpoint.

var linkRun struct {
	mu      sync.Mutex
	running bool
	last    *linkdisc.Report
	lastErr string
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.Store.ListDiscoveredLinks()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if links == nil {
		links = []store.DiscoveredLink{}
	}
	writeJSON(w, 200, links)
}

func (s *Server) linksStatus(w http.ResponseWriter, r *http.Request) {
	linkRun.mu.Lock()
	defer linkRun.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"running": linkRun.running, "last_report": linkRun.last, "last_error": linkRun.lastErr,
	})
}

func (s *Server) runLinkDiscovery(w http.ResponseWriter, r *http.Request) {
	linkRun.mu.Lock()
	if linkRun.running {
		linkRun.mu.Unlock()
		writeJSON(w, 202, map[string]any{"ok": true, "message": "a discovery run is already in progress"})
		return
	}
	linkRun.running = true
	linkRun.mu.Unlock()
	go func() {
		rep, err := linkdisc.Run(s.Store)
		linkRun.mu.Lock()
		linkRun.running = false
		if err != nil {
			linkRun.lastErr = err.Error()
		} else {
			linkRun.last = rep
			linkRun.lastErr = ""
		}
		linkRun.mu.Unlock()
	}()
	writeJSON(w, 202, map[string]any{"ok": true, "message": "link discovery started"})
}

// topologyFromLinks builds a new topology from the discovered links:
// every device that takes part in a link becomes a node (grid layout),
// every unique device pair becomes one link labelled with the best
// evidence (LLDP > CDP > FDB) and the port pair.
func (s *Server) topologyFromLinks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if r.Body != nil { // body is optional; an absent/empty one is fine
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	links, err := s.Store.ListDiscoveredLinks()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if len(links) == 0 {
		errJSON(w, 400, "no discovered links yet — run link discovery first")
		return
	}
	name := req.Name
	if name == "" {
		name = "Discovered links"
	}
	rank := map[string]int{"LLDP": 0, "CDP": 1, "FDB": 2}
	best := map[string]store.DiscoveredLink{}
	devicesSeen := map[int64]bool{}
	for _, l := range links {
		key := fmt.Sprintf("%d-%d", l.DeviceA, l.DeviceB)
		devicesSeen[l.DeviceA] = true
		devicesSeen[l.DeviceB] = true
		if cur, ok := best[key]; !ok || rank[l.Via] < rank[cur.Via] {
			best[key] = l
		}
	}
	tid, err := s.Store.CreateTopology(name, "discovery")
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	// Grid layout, ~5 columns, same spacing as the Excel auto-layout.
	ids := make([]int64, 0, len(devicesSeen))
	for id := range devicesSeen {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ { // small insertion sort for determinism
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	nodeOf := map[int64]int64{}
	for i, devID := range ids {
		dev, err := s.Store.GetDevice(devID)
		if err != nil {
			_ = s.Store.DeleteTopology(tid)
			errJSON(w, 500, err.Error())
			return
		}
		x := 90 + float64(i%5)*190
		y := 80 + float64(i/5)*140
		nid, err := s.Store.AddTopologyNode(store.TopologyNode{
			TopologyID: tid, Label: dev.Name, IP: dev.IP, Type: dev.Type,
			DeviceID: &devID, X: math.Round(x), Y: math.Round(y),
		})
		if err != nil {
			_ = s.Store.DeleteTopology(tid)
			errJSON(w, 500, err.Error())
			return
		}
		nodeOf[devID] = nid
	}
	added := 0
	for _, l := range best {
		label := l.Via
		if l.IfA != "" && l.IfB != "" {
			label = fmt.Sprintf("%s · %s ↔ %s", l.Via, l.IfA, l.IfB)
		}
		if _, err := s.Store.AddTopologyLink(store.TopologyLink{
			TopologyID: tid, FromNode: nodeOf[l.DeviceA], ToNode: nodeOf[l.DeviceB], Label: label,
		}); err != nil {
			_ = s.Store.DeleteTopology(tid)
			errJSON(w, 500, err.Error())
			return
		}
		added++
	}
	writeJSON(w, 201, map[string]any{"id": tid, "nodes": len(ids), "links": added})
}
