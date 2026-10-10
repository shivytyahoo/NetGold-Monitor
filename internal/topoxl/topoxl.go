// Package topoxl reads and writes NetGold topologies as Excel workbooks
// (v2.0). The format has two sheets:
//
//	Nodes: Name | IP | Type | Group | X | Y     (X/Y optional)
//	Links: From | To | Label | Bandwidth        (From/To are node names)
//
// The same format is used for the downloadable template, for imports and
// for exports, so an exported workbook can be edited and re-imported.
package topoxl

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"netgold/internal/store"
)

var nodeHeaders = []string{"Name", "IP", "Type", "Group", "X", "Y"}
var linkHeaders = []string{"From", "To", "Label", "Bandwidth"}

// ParsedLink is a link whose endpoints are still node names (resolution
// to node IDs happens when the topology is written to the store).
type ParsedLink struct {
	FromName  string
	ToName    string
	Label     string
	Bandwidth string
}

// Parsed is the result of reading a workbook.
type Parsed struct {
	Nodes []store.TopologyNode // X/Y are 0 when the sheet has none
	HasXY []bool               // per node: true when X and Y were given
	Links []ParsedLink
}

// AutoLayout places nodes without coordinates on a deterministic coarse
// grid (no two nodes share a cell, so nothing overlaps). Nodes are laid
// out in slice order, left-to-right, top-to-bottom.
func AutoLayout(nodes []store.TopologyNode, hasXY []bool) {
	const cellW, cellH = 190.0, 120.0
	missing := 0
	for i := range nodes {
		if i < len(hasXY) && !hasXY[i] {
			missing++
		}
	}
	if missing == 0 {
		return
	}
	cols := int(math.Ceil(math.Sqrt(float64(missing))))
	if cols < 1 {
		cols = 1
	}
	slot := 0
	for i := range nodes {
		if i < len(hasXY) && hasXY[i] {
			continue
		}
		nodes[i].X = 90 + float64(slot%cols)*cellW
		nodes[i].Y = 80 + float64(slot/cols)*cellH
		slot++
	}
}

// Parse reads a topology workbook from r.
func Parse(r io.Reader) (*Parsed, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, fmt.Errorf("open workbook: %w", err)
	}
	defer f.Close()
	out := &Parsed{}

	nodeRows, err := f.GetRows("Nodes")
	if err != nil {
		return nil, fmt.Errorf("the workbook has no \"Nodes\" sheet")
	}
	for i, row := range nodeRows {
		if i == 0 || isHeader(row, "name") {
			continue
		}
		get := func(idx int) string {
			if idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
			return ""
		}
		name := get(0)
		if name == "" {
			continue
		}
		n := store.TopologyNode{Label: name, IP: get(1), Type: get(2), GroupName: get(3)}
		has := false
		if xs, ys := get(4), get(5); xs != "" && ys != "" {
			if x, err1 := strconv.ParseFloat(xs, 64); err1 == nil {
				if y, err2 := strconv.ParseFloat(ys, 64); err2 == nil {
					n.X, n.Y, has = x, y, true
				}
			}
		}
		out.Nodes = append(out.Nodes, n)
		out.HasXY = append(out.HasXY, has)
	}
	if len(out.Nodes) == 0 {
		return nil, fmt.Errorf("the \"Nodes\" sheet contains no nodes")
	}

	linkRows, err := f.GetRows("Links")
	if err == nil {
		known := map[string]bool{}
		for _, n := range out.Nodes {
			known[n.Label] = true
		}
		for i, row := range linkRows {
			if i == 0 || isHeader(row, "from") {
				continue
			}
			get := func(idx int) string {
				if idx < len(row) {
					return strings.TrimSpace(row[idx])
				}
				return ""
			}
			from, to := get(0), get(1)
			if from == "" || to == "" {
				continue
			}
			if !known[from] {
				return nil, fmt.Errorf("Links row %d: node %q is not in the Nodes sheet", i+1, from)
			}
			if !known[to] {
				return nil, fmt.Errorf("Links row %d: node %q is not in the Nodes sheet", i+1, to)
			}
			out.Links = append(out.Links, ParsedLink{FromName: from, ToName: to, Label: get(2), Bandwidth: get(3)})
		}
	}
	return out, nil
}

func isHeader(row []string, first string) bool {
	return len(row) > 0 && strings.EqualFold(strings.TrimSpace(row[0]), first)
}

// Import writes a parsed workbook into the store as a new topology and
// returns the new topology ID. Auto-layout is applied to nodes without
// coordinates first.
func Import(st *store.Store, name, source string, p *Parsed) (int64, error) {
	nodes := make([]store.TopologyNode, len(p.Nodes))
	copy(nodes, p.Nodes)
	AutoLayout(nodes, p.HasXY)
	tid, err := st.CreateTopology(name, source)
	if err != nil {
		return 0, err
	}
	byName := map[string]int64{}
	for _, n := range nodes {
		n.TopologyID = tid
		// Link the node to a monitored device when the IP matches.
		if n.IP != "" {
			if d, err := st.DeviceByIP(n.IP); err == nil {
				id := d.ID
				n.DeviceID = &id
			}
		}
		id, err := st.AddTopologyNode(n)
		if err != nil {
			_ = st.DeleteTopology(tid)
			return 0, err
		}
		byName[n.Label] = id
	}
	for _, l := range p.Links {
		if _, err := st.AddTopologyLink(store.TopologyLink{
			TopologyID: tid, FromNode: byName[l.FromName], ToNode: byName[l.ToName],
			Label: l.Label, Bandwidth: l.Bandwidth,
		}); err != nil {
			_ = st.DeleteTopology(tid)
			return 0, err
		}
	}
	return tid, nil
}

func writeWorkbook(nodes []store.TopologyNode, links []ParsedLink, example bool) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	ns, err := f.NewSheet("Nodes")
	if err != nil {
		return nil, err
	}
	f.DeleteSheet("Sheet1")
	if _, err := f.NewSheet("Links"); err != nil {
		return nil, err
	}
	f.SetActiveSheet(ns)
	for c, h := range nodeHeaders {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		f.SetCellValue("Nodes", cell, h)
	}
	for c, h := range linkHeaders {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		f.SetCellValue("Links", cell, h)
	}
	for i, n := range nodes {
		r := i + 2
		f.SetCellValue("Nodes", fmt.Sprintf("A%d", r), n.Label)
		f.SetCellValue("Nodes", fmt.Sprintf("B%d", r), n.IP)
		f.SetCellValue("Nodes", fmt.Sprintf("C%d", r), n.Type)
		f.SetCellValue("Nodes", fmt.Sprintf("D%d", r), n.GroupName)
		// Example/template rows leave X/Y blank to demonstrate optional
		// coordinates; exports always carry the real positions.
		if !example {
			f.SetCellValue("Nodes", fmt.Sprintf("E%d", r), n.X)
			f.SetCellValue("Nodes", fmt.Sprintf("F%d", r), n.Y)
		}
	}
	for i, l := range links {
		r := i + 2
		f.SetCellValue("Links", fmt.Sprintf("A%d", r), l.FromName)
		f.SetCellValue("Links", fmt.Sprintf("B%d", r), l.ToName)
		f.SetCellValue("Links", fmt.Sprintf("C%d", r), l.Label)
		f.SetCellValue("Links", fmt.Sprintf("D%d", r), l.Bandwidth)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Template returns the downloadable example workbook: two example nodes
// and one link, in exactly the import format.
func Template() ([]byte, error) {
	nodes := []store.TopologyNode{
		{Label: "Core Router", IP: "192.168.1.1", Type: "router", GroupName: "Head Office"},
		{Label: "Access Switch", IP: "192.168.1.2", Type: "switch", GroupName: "Head Office"},
	}
	links := []ParsedLink{{FromName: "Core Router", ToName: "Access Switch", Label: "Uplink", Bandwidth: "1 Gbps"}}
	return writeWorkbook(nodes, links, true)
}

// Export writes a stored topology as a workbook in the import format.
func Export(st *store.Store, topologyID int64) ([]byte, error) {
	nodes, err := st.ListTopologyNodes(topologyID)
	if err != nil {
		return nil, err
	}
	links, err := st.ListTopologyLinks(topologyID)
	if err != nil {
		return nil, err
	}
	nameByID := map[int64]string{}
	for _, n := range nodes {
		nameByID[n.ID] = n.Label
	}
	pl := make([]ParsedLink, 0, len(links))
	for _, l := range links {
		pl = append(pl, ParsedLink{FromName: nameByID[l.FromNode], ToName: nameByID[l.ToNode],
			Label: l.Label, Bandwidth: l.Bandwidth})
	}
	return writeWorkbook(nodes, pl, false)
}
