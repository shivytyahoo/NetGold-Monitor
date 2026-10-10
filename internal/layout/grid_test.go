package layout

import (
	"math"
	"reflect"
	"testing"
)

// reservedRect is the rectangle a point keeps clear in grid
// mode: FW wide centred on the point, from FH below the centre
// to gridMarkerTop above it.
func reservedRect(p Point) [4]float64 {
	return [4]float64{p.X - p.FW/2, p.Y - p.FH, p.X + p.FW/2, p.Y + gridMarkerTop}
}

func rectsOverlap(a, b [4]float64) bool {
	return a[0] < b[2] && b[0] < a[2] && a[1] < b[3] && b[1] < a[3]
}

func rectInside(inner, outer [4]float64) bool {
	return inner[0] >= outer[0]-1e-9 && inner[1] >= outer[1]-1e-9 &&
		inner[2] <= outer[2]+1e-9 && inner[3] <= outer[3]+1e-9
}

// gridFixture builds a small drawing with a parent box holding
// devices directly plus two child boxes, a sibling box, loose
// points, and links both inside boxes and across them.
func gridFixture() ([]Point, []GridBox, [][]int) {
	mk := func(id int64, x, y, w, reach float64) Point {
		return Point{ID: id, X: x, Y: y, FW: w, FH: reach}
	}
	pts := []Point{
		mk(1, 0, 0, 90, 70),     // child A
		mk(2, 30, 10, 80, 70),   // child A
		mk(3, 60, -5, 100, 70),  // child A
		mk(4, 300, 200, 85, 70), // child B
		mk(5, 330, 190, 95, 70), // child B
		mk(6, 150, 300, 90, 70), // parent, direct
		mk(7, 180, 320, 90, 70), // parent, direct
		mk(8, 120, 280, 90, 70), // parent, direct
		mk(9, 700, 100, 90, 70), // sibling box
		mk(10, 730, 120, 90, 70),
		mk(11, -300, -200, 90, 70), // loose
		mk(12, -260, -180, 90, 70), // loose
	}
	boxes := []GridBox{
		{ID: 101, Members: []int{5, 6, 7}, Env: [4]float64{-100, -100, 500, 400}, Parent: -1}, // parent (points 6,7,8 = idx 5,6,7)
		{ID: 102, Members: []int{0, 1, 2}, Env: [4]float64{-50, -60, 120, 60}, Parent: 0},     // child A
		{ID: 103, Members: []int{3, 4}, Env: [4]float64{260, 140, 380, 260}, Parent: 0},       // child B
		{ID: 104, Members: []int{8, 9}, Env: [4]float64{650, 40, 800, 180}, Parent: -1},       // sibling
	}
	links := [][]int{{0, 1}, {1, 2}, {2, 6}, {3, 4}, {4, 5}, {8, 9}, {9, 10}, {10, 11}}
	return pts, boxes, links
}

func TestTidyGridDeterministic(t *testing.T) {
	pts, boxes, links := gridFixture()
	a := TidyGrid(pts, 64, 98, boxes, links, Options{Aspect: 1.6})
	b := TidyGrid(pts, 64, 98, boxes, links, Options{Aspect: 1.6})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("grid layout is not deterministic:\n%v\nvs\n%v", a, b)
	}
}

func TestTidyGridInvariants(t *testing.T) {
	pts, boxes, links := gridFixture()
	res := TidyGrid(pts, 64, 98, boxes, links, Options{Aspect: 1.6})
	out := res.Points

	// Every point keeps its ID and gets a cell width.
	for i, p := range out {
		if p.ID != pts[i].ID {
			t.Fatalf("point %d: ID changed %d -> %d", i, pts[i].ID, p.ID)
		}
		if res.CellW[i] <= 0 {
			t.Fatalf("point %d: no cell width", i)
		}
	}
	// No two reserved rectangles overlap.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if rectsOverlap(reservedRect(out[i]), reservedRect(out[j])) {
				t.Fatalf("reserved rectangles of points %d and %d overlap: %v vs %v",
					i, j, reservedRect(out[i]), reservedRect(out[j]))
			}
		}
	}
	// Every member's reserved rectangle sits inside its box's
	// drawn rectangle; child boxes sit inside their parent.
	owner := map[int]int{}
	for bi, b := range boxes {
		for _, m := range b.Members {
			owner[m] = bi
		}
	}
	for m, bi := range owner {
		if !rectInside(reservedRect(out[m]), res.Boxes[bi]) {
			t.Fatalf("point %d reserved rect %v escapes its box %v", m, reservedRect(out[m]), res.Boxes[bi])
		}
	}
	for bi, b := range boxes {
		if b.Parent >= 0 && !rectInside(res.Boxes[bi], res.Boxes[b.Parent]) {
			t.Fatalf("box %d rect %v escapes parent %v", bi, res.Boxes[bi], res.Boxes[b.Parent])
		}
	}
	// Sibling boxes (same parent) never overlap.
	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			if boxes[i].Parent == boxes[j].Parent && rectsOverlap(res.Boxes[i], res.Boxes[j]) {
				t.Fatalf("sibling boxes %d and %d overlap: %v vs %v", i, j, res.Boxes[i], res.Boxes[j])
			}
		}
	}
	// Loose points' reserved rectangles stay out of every box.
	for i := range out {
		if _, ok := owner[i]; ok {
			continue
		}
		for bi := range boxes {
			if rectsOverlap(reservedRect(out[i]), res.Boxes[bi]) {
				t.Fatalf("loose point %d reserved rect %v overlaps box %d %v", i, reservedRect(out[i]), bi, res.Boxes[bi])
			}
		}
	}
}

// TestTidyGridLinkAdjacency checks that linked members of one box
// land in neighbouring cells: their centre distance stays within
// one diagonal pitch of the box's grid.
func TestTidyGridLinkAdjacency(t *testing.T) {
	pts, boxes, links := gridFixture()
	res := TidyGrid(pts, 64, 98, boxes, links, Options{Aspect: 1.6})
	out := res.Points
	// In-box links: (0,1), (1,2) in child A; (3,4) in child B; (8,9) sibling.
	for _, lk := range [][2]int{{0, 1}, {1, 2}, {3, 4}, {8, 9}} {
		a, b := out[lk[0]], out[lk[1]]
		d := math.Hypot(a.X-b.X, a.Y-b.Y)
		// pitchX <= 110, pitchY <= 108 in this fixture
		if d > 160 {
			t.Fatalf("linked points %d,%d are %v apart — not neighbouring cells", lk[0], lk[1], d)
		}
	}
}

// TestTidyGridEdgeCases: one point, no boxes, an empty box, a
// box whose parent pointer is invalid or cyclic.
func TestTidyGridEdgeCases(t *testing.T) {
	one := []Point{{ID: 7, X: 5, Y: 6, FW: 80, FH: 60}}
	res := TidyGrid(one, 64, 98, nil, nil, Options{})
	if len(res.Points) != 1 || res.CellW[0] <= 0 {
		t.Fatalf("single loose point: %+v", res)
	}
	boxes := []GridBox{
		{ID: 1, Members: []int{0}, Env: [4]float64{0, 0, 100, 100}, Parent: 1}, // self-parent -> root
		{ID: 2, Members: nil, Env: [4]float64{200, 200, 300, 260}, Parent: 0},  // empty child
	}
	res = TidyGrid(one, 64, 98, boxes, nil, Options{})
	if !rectInside(reservedRect(res.Points[0]), res.Boxes[0]) {
		t.Fatalf("point escapes its box: %v vs %v", reservedRect(res.Points[0]), res.Boxes[0])
	}
	if !rectInside(res.Boxes[1], res.Boxes[0]) {
		t.Fatalf("empty child box escapes parent: %v vs %v", res.Boxes[1], res.Boxes[0])
	}
	cyc := []GridBox{
		{ID: 1, Members: nil, Env: [4]float64{0, 0, 100, 100}, Parent: 1 - 1}, // -> box 1... indexes: parent 0 is itself below
		{ID: 2, Members: nil, Env: [4]float64{10, 10, 50, 50}, Parent: 0},
	}
	cyc[0].Parent = 1 // 0 -> 1 -> 0 cycle
	res = TidyGrid(one, 64, 98, cyc, nil, Options{})
	if len(res.Boxes) != 2 {
		t.Fatalf("cyclic parents: got %d boxes", len(res.Boxes))
	}
}

// The grid footprint is asymmetric (label below the marker), so
// two points in adjacent rows across a box border can sit closer
// than the heuristic minima on the vertical axis while their
// reserved rectangles still clear: GridViolations must report 0
// for them, and must still catch a genuine overlap.
func TestGridViolationsSemantics(t *testing.T) {
	// Centres 94 apart vertically, footprints 100 wide, label
	// reach 47 below centre, marker reserve 25 above: reserved
	// rectangles clear by 94-47-25 = 22.
	clear := []Point{
		{X: 0, Y: 0, FW: 100, FH: 47},
		{X: 38, Y: -94, FW: 100, FH: 47},
	}
	if v := GridViolations(clear); v != 0 {
		t.Errorf("clearing pair counted %d violations, want 0", v)
	}
	hit := []Point{
		{X: 0, Y: 0, FW: 100, FH: 47},
		{X: 10, Y: -60, FW: 100, FH: 47},
	}
	if v := GridViolations(hit); v != 1 {
		t.Errorf("overlapping pair counted %d violations, want 1", v)
	}
}

// Regression: boxes may be declared in any order — callers send
// them smallest-first, so children precede their parents. The
// parent wiring must not depend on declaration order: a child
// declared before its parent still nests inside it. (An earlier
// cycle guard read unassigned parent entries as pointers to box
// 0 and silently demoted such children to roots.)
func TestTidyGridChildrenDeclaredFirst(t *testing.T) {
	pts, boxes, links := gridFixture()
	// Reorder: children (102, 103) and the sibling (104) first,
	// the parent (101) last; remap parent indices accordingly.
	reorder := []int{1, 2, 3, 0}
	nb := make([]GridBox, len(boxes))
	for newIdx, oldIdx := range reorder {
		b := boxes[oldIdx]
		if b.Parent >= 0 {
			for ni, oi := range reorder {
				if oi == b.Parent {
					b.Parent = ni
				}
			}
		}
		nb[newIdx] = b
	}
	res := TidyGrid(pts, 64, 98, nb, links, Options{Aspect: 1.6})
	// Old box 0 (the parent) sits at new index 3; its children at
	// new indices 0 and 1 must nest inside it.
	if !rectInside(res.Boxes[0], res.Boxes[3]) {
		t.Fatalf("child box 0 rect %v escapes parent %v (declared after it)", res.Boxes[0], res.Boxes[3])
	}
	if !rectInside(res.Boxes[1], res.Boxes[3]) {
		t.Fatalf("child box 1 rect %v escapes parent %v (declared after it)", res.Boxes[1], res.Boxes[3])
	}
	// Every member's reserved rectangle inside its box, as usual.
	owner := map[int]int{}
	for bi, b := range nb {
		for _, m := range b.Members {
			owner[m] = bi
		}
	}
	for m, bi := range owner {
		if !rectInside(reservedRect(res.Points[m]), res.Boxes[bi]) {
			t.Fatalf("point %d reserved rect %v escapes its box %v", m, reservedRect(res.Points[m]), res.Boxes[bi])
		}
	}
}

// A box stored as an extreme tall strip (a column of devices
// drawn for a tall window) must not produce an equally extreme
// grid block: the column count is nudged until the block's own
// aspect enters the band a screen can hold, and the layout's
// invariants still hold.
func TestTidyGridTallStripBox(t *testing.T) {
	var pts []Point
	var members []int
	for i := 0; i < 18; i++ {
		pts = append(pts, Point{ID: int64(i + 1), X: float64(i % 2), Y: float64(i * 10), FW: 88, FH: 47})
		members = append(members, i)
	}
	boxes := []GridBox{
		{ID: 1, Members: members, Env: [4]float64{0, 0, 60, 900}, Parent: -1}, // aspect 0.067
	}
	res := TidyGrid(pts, 64, 98, boxes, nil, Options{Aspect: 1.69})
	r := res.Boxes[0]
	aspect := (r[2] - r[0]) / (r[3] - r[1])
	if aspect < gridBlockAspectMin-0.15 || aspect > gridBlockAspectMax+0.15 {
		t.Fatalf("tall strip produced block aspect %.2f, outside the band [%.2f, %.2f]", aspect, gridBlockAspectMin, gridBlockAspectMax)
	}
	if v := GridViolations(res.Points); v != 0 {
		t.Fatalf("tall strip layout has %d grid violations", v)
	}
	for _, m := range members {
		if !rectInside(reservedRect(res.Points[m]), r) {
			t.Fatalf("point %d reserved rect %v escapes tall-strip box %v", m, reservedRect(res.Points[m]), r)
		}
	}
}

// With a canvas named, the column optimisation must improve
// the whole scene's fit ratio over the aspect-derived counts
// on a row of tall boxes (each box is better slightly wider
// when its siblings stand beside it), and the optimised run
// must stay deterministic and violation-free.
func TestTidyGridSceneFitOptimisation(t *testing.T) {
	var pts []Point
	var boxes []GridBox
	id := int64(1)
	for b := 0; b < 3; b++ {
		var members []int
		for i := 0; i < 24; i++ {
			pts = append(pts, Point{ID: id, X: float64(b*1000 + i%3), Y: float64(i * 40), FW: 88, FH: 47})
			id++
			members = append(members, len(pts)-1)
		}
		boxes = append(boxes, GridBox{
			ID: int64(b + 1), Members: members,
			Env:    [4]float64{float64(b * 1000), 0, float64(b*1000) + 120, 960}, // tall strips in a row
			Parent: -1,
		})
	}
	plain := TidyGrid(pts, 64, 98, boxes, nil, Options{Aspect: 1.69})
	fitPlain := gridSceneFit(plain, 1280, 760)
	opt := TidyGrid(pts, 64, 98, boxes, nil, Options{Aspect: 1.69, BoxW: 1280, BoxH: 760})
	fitOpt := gridSceneFit(opt, 1280, 760)
	if fitOpt <= fitPlain+0.0015 {
		t.Fatalf("scene fit did not improve: plain %.4f optimised %.4f", fitPlain, fitOpt)
	}
	if v := GridViolations(opt.Points); v != 0 {
		t.Fatalf("optimised layout has %d grid violations", v)
	}
	again := TidyGrid(pts, 64, 98, boxes, nil, Options{Aspect: 1.69, BoxW: 1280, BoxH: 760})
	if !reflect.DeepEqual(opt, again) {
		t.Fatal("optimised layout is not deterministic")
	}
}
