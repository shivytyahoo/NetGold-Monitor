package topoxl

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"

	"netgold/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	return st
}

// makeWorkbook builds a sample import file: 3 nodes (one with explicit
// coordinates, two without) and 2 links.
func makeWorkbook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	f.NewSheet("Nodes")
	f.DeleteSheet("Sheet1")
	f.NewSheet("Links")
	for c, h := range nodeHeaders {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		f.SetCellValue("Nodes", cell, h)
	}
	for c, h := range linkHeaders {
		cell, _ := excelize.CoordinatesToCellName(c+1, 1)
		f.SetCellValue("Links", cell, h)
	}
	rows := [][]any{
		{"Core Router", "10.0.0.1", "router", "HQ", 500, 60},
		{"Switch A", "10.0.0.2", "switch", "HQ", nil, nil},
		{"Switch B", "10.0.0.3", "switch", "HQ", nil, nil},
	}
	for i, r := range rows {
		for c, v := range r {
			if v == nil {
				continue
			}
			cell, _ := excelize.CoordinatesToCellName(c+1, i+2)
			f.SetCellValue("Nodes", cell, v)
		}
	}
	f.SetCellValue("Links", "A2", "Core Router")
	f.SetCellValue("Links", "B2", "Switch A")
	f.SetCellValue("Links", "C2", "Uplink")
	f.SetCellValue("Links", "D2", "1 Gbps")
	f.SetCellValue("Links", "A3", "Core Router")
	f.SetCellValue("Links", "B3", "Switch B")
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExcelRoundTrip(t *testing.T) {
	st := openStore(t)
	data := makeWorkbook(t)

	p, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Nodes) != 3 || len(p.Links) != 2 {
		t.Fatalf("parsed nodes=%d links=%d, want 3/2", len(p.Nodes), len(p.Links))
	}
	tid, err := Import(st, "Round Trip", "excel", p)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	top, _ := st.GetTopology(tid)
	if top.Nodes != 3 || top.Links != 2 || top.Source != "excel" {
		t.Fatalf("stored topology: %+v", top)
	}
	nodes, _ := st.ListTopologyNodes(tid)
	// The explicitly positioned node keeps its coordinates.
	// The two auto-layout nodes must not overlap each other or it.
	seen := map[[2]float64]string{}
	for _, n := range nodes {
		key := [2]float64{n.X, n.Y}
		if prev, dup := seen[key]; dup {
			t.Errorf("nodes %q and %q overlap at %v", prev, n.Label, key)
		}
		seen[key] = n.Label
	}
	for _, n := range nodes {
		if n.Label == "Core Router" && (n.X != 500 || n.Y != 60) {
			t.Errorf("explicit position changed: %+v", n)
		}
	}

	// Export and re-import: counts must be identical.
	out, err := Export(st, tid)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	p2, err := Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-parse export: %v", err)
	}
	if len(p2.Nodes) != 3 || len(p2.Links) != 2 {
		t.Fatalf("round trip counts: nodes=%d links=%d, want 3/2", len(p2.Nodes), len(p2.Links))
	}
	tid2, err := Import(st, "Round Trip 2", "excel", p2)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	top2, _ := st.GetTopology(tid2)
	if top2.Nodes != 3 || top2.Links != 2 {
		t.Fatalf("re-imported topology: %+v", top2)
	}
}

func TestTemplateOpensAndParses(t *testing.T) {
	data, err := Template()
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("template does not open in excelize: %v", err)
	}
	f.Close()
	p, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	if len(p.Nodes) != 2 || len(p.Links) != 1 {
		t.Fatalf("template nodes=%d links=%d, want 2/1", len(p.Nodes), len(p.Links))
	}
}

func TestParseRejectsUnknownLinkEndpoint(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	f.NewSheet("Nodes")
	f.DeleteSheet("Sheet1")
	f.NewSheet("Links")
	f.SetCellValue("Nodes", "A1", "Name")
	f.SetCellValue("Nodes", "A2", "Only Node")
	f.SetCellValue("Links", "A1", "From")
	f.SetCellValue("Links", "A2", "Only Node")
	f.SetCellValue("Links", "B2", "Ghost Node")
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(&buf); err == nil {
		t.Fatal("parse accepted a link to a node that does not exist")
	}
}

func TestAutoLayoutDeterministicAndNonOverlapping(t *testing.T) {
	nodes := make([]store.TopologyNode, 9)
	has := make([]bool, 9)
	AutoLayout(nodes, has)
	seen := map[[2]float64]bool{}
	for i, n := range nodes {
		key := [2]float64{n.X, n.Y}
		if seen[key] {
			t.Fatalf("node %d overlaps at %v", i, key)
		}
		seen[key] = true
	}
	nodes2 := make([]store.TopologyNode, 9)
	AutoLayout(nodes2, has)
	for i := range nodes {
		if nodes[i] != nodes2[i] {
			t.Fatalf("layout not deterministic at %d: %+v vs %+v", i, nodes[i], nodes2[i])
		}
	}
}
