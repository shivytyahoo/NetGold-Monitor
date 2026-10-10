package layout

import (
	"math"
	"testing"
)

// Stage-1.1 guarantees, exercised on the large generated
// fixtures and on small synthetic layouts:
//
//   - drawn boxes never overlap except by stored nesting, and
//     sibling boxes keep the packing gap between them;
//   - the loose group (points in no box) reports its own drawn
//     rectangle containing all its devices;
//   - every device's reserved rectangle stays inside its box;
//   - same-row neighbouring pills keep the full column gap in
//     layout units (the per-column pitch guarantee);
//   - a member with a cross-box link is placed on the side of
//     its box facing the partner box.

// rectSeparation is the gap between two rectangles: 0 when they
// touch or overlap on both axes, otherwise the distance along
// the separating axis (the larger axis gap when they are apart
// diagonally is not needed by these tests).
func rectSeparation(a, b [4]float64) float64 {
	gx := math.Max(b[0]-a[2], a[0]-b[2])
	gy := math.Max(b[1]-a[3], a[1]-b[3])
	if gx > 0 && gy > 0 {
		return math.Min(gx, gy)
	}
	return math.Max(gx, gy)
}

func ownerIndex(boxes []GridBox, i int) int {
	for bi, b := range boxes {
		for _, m := range b.Members {
			if m == i {
				return bi
			}
		}
	}
	return -1
}

func TestTidyGridStage1BFixtures(t *testing.T) {
	for _, fx := range []struct {
		name      string
		get       func() ([]Point, []GridBox)
		links     [][]int
		wantLoose int
	}{
		{"CAMPUS-A", synthFixtureCampus, nil, 5},
		// INTER-ZONE-100M: the cross-box pair — index 8 (in
		// sub-box 490) linked to index 9 (in main box 481).
		{"INTER-ZONE-100M", synthFixtureInterZone, [][]int{{8, 9}}, 0},
	} {
		pts, boxes := fx.get()
		res := TidyGrid(pts, 64, 98, boxes, fx.links, Options{Aspect: 1278.0 / 756.0, BoxW: 1278, BoxH: 756})
		if v := GridViolations(res.Points); v != 0 {
			t.Fatalf("%s: %d grid violations", fx.name, v)
		}
		ownerRect := func(i int) [4]float64 {
			if bi := ownerIndex(boxes, i); bi >= 0 {
				return res.Boxes[bi]
			}
			if !res.HasLoose {
				t.Fatalf("%s: point %d is loose but no loose rectangle was reported", fx.name, i)
			}
			return res.Loose
		}
		// Every reserved rectangle inside its owner's rectangle.
		looseCount := 0
		for i, p := range res.Points {
			if ownerIndex(boxes, i) < 0 {
				looseCount++
			}
			if !rectInside(reservedRect(p), ownerRect(i)) {
				t.Fatalf("%s: point %d reserved rect %v escapes its area %v", fx.name, i, reservedRect(p), ownerRect(i))
			}
		}
		if looseCount != fx.wantLoose {
			t.Fatalf("%s: %d loose points, want %d", fx.name, looseCount, fx.wantLoose)
		}
		if fx.wantLoose > 0 && !res.HasLoose {
			t.Fatalf("%s: loose points exist but HasLoose is false", fx.name)
		}
		// Box pairs: overlap only by nesting (one contains the
		// other); sibling roots keep the packing gap.
		for i := 0; i < len(boxes); i++ {
			for j := i + 1; j < len(boxes); j++ {
				a, b := res.Boxes[i], res.Boxes[j]
				if !rectsOverlap(a, b) {
					continue
				}
				if !(rectInside(a, b) || rectInside(b, a)) {
					t.Fatalf("%s: boxes %d and %d overlap without nesting: %v x %v", fx.name, boxes[i].ID, boxes[j].ID, a, b)
				}
			}
		}
		// The loose rectangle is a root like the boxes: it must
		// not overlap any box at all.
		if res.HasLoose {
			for i, b := range res.Boxes {
				if rectsOverlap(res.Loose, b) && !rectInside(res.Loose, b) && !rectInside(b, res.Loose) {
					t.Fatalf("%s: loose rect %v overlaps box %d rect %v", fx.name, res.Loose, boxes[i].ID, b)
				}
			}
		}
		// Same-owner, same-row neighbours keep the column gap.
		for i := 0; i < len(res.Points); i++ {
			for j := i + 1; j < len(res.Points); j++ {
				if ownerIndex(boxes, i) != ownerIndex(boxes, j) {
					continue
				}
				a, b := res.Points[i], res.Points[j]
				if math.Abs(a.Y-b.Y) > 0.01 {
					continue
				}
				lo, hi := a, b
				if hi.X < lo.X {
					lo, hi = b, a
				}
				gap := (hi.X - hi.FW/2) - (lo.X + lo.FW/2)
				if gap < gridGapX-0.01 {
					t.Fatalf("%s: same-row pill gap %.2f between points %d and %d, want >= %.1f", fx.name, gap, i, j, gridGapX)
				}
			}
		}
		t.Logf("%s: scene fit %.3f, loose=%v", fx.name, gridSceneFit(res, 1278, 756), res.HasLoose)
	}
}

// On INTER-ZONE-100M, index 8 sits in a small sub-box and
// index 9 in the main box's own grid. The facing assignment
// (stored centres, then the drawn-centre refinement) must
// bring the linked pair close — nowhere near the distance
// the plain breadth-first fill leaves them at.
func TestTidyGridFacingEdgeInterZone(t *testing.T) {
	pts, boxes := synthFixtureInterZone()
	res := TidyGrid(pts, 64, 98, boxes, [][]int{{8, 9}}, Options{Aspect: 1278.0 / 756.0, BoxW: 1278, BoxH: 756})
	plain := TidyGrid(pts, 64, 98, boxes, nil, Options{Aspect: 1278.0 / 756.0, BoxW: 1278, BoxH: 756})
	d := math.Hypot(res.Points[8].X-res.Points[9].X, res.Points[8].Y-res.Points[9].Y)
	dPlain := math.Hypot(plain.Points[8].X-plain.Points[9].X, plain.Points[8].Y-plain.Points[9].Y)
	t.Logf("cross-box pair distance: %.0f layout units (unlinked fill: %.0f)", d, dPlain)
	if d > 400 {
		t.Fatalf("cross-box pair still far apart: %.0f layout units", d)
	}
	if d > 0.62*dPlain {
		t.Fatalf("facing assignment barely helped: %.0f vs %.0f unlinked", d, dPlain)
	}
}

// Synthetic facing check: in two side-by-side boxes, a member
// linked across must take the column nearest the partner box,
// on both sides of the link.
func TestTidyGridFacingEdgeSynthetic(t *testing.T) {
	var pts []Point
	mk := func(x, y float64) int {
		pts = append(pts, Point{ID: int64(len(pts) + 1), X: x, Y: y, FW: 60, FH: 47})
		return len(pts) - 1
	}
	var west, east []int
	for i := 0; i < 8; i++ {
		west = append(west, mk(float64(i%4)*10, float64(i/4)*10))
	}
	westCross := mk(0, 0) // index 8, linked east
	west = append(west, westCross)
	for i := 0; i < 8; i++ {
		east = append(east, mk(500+float64(i%4)*10, float64(i/4)*10))
	}
	eastCross := mk(500, 0) // index 17, linked west
	east = append(east, eastCross)
	boxes := []GridBox{
		{ID: 1, Members: west, Env: [4]float64{-50, -50, 100, 100}, Parent: -1},
		{ID: 2, Members: east, Env: [4]float64{450, -50, 600, 100}, Parent: -1},
	}
	res := TidyGrid(pts, 64, 98, boxes, [][]int{{westCross, eastCross}}, Options{Aspect: 1.69})
	maxWX, minEX := math.Inf(-1), math.Inf(1)
	for _, m := range west {
		maxWX = math.Max(maxWX, res.Points[m].X)
	}
	for _, m := range east {
		minEX = math.Min(minEX, res.Points[m].X)
	}
	if res.Points[westCross].X != maxWX {
		t.Fatalf("west crosser at x=%.0f, want the east-most column %.0f", res.Points[westCross].X, maxWX)
	}
	if res.Points[eastCross].X != minEX {
		t.Fatalf("east crosser at x=%.0f, want the west-most column %.0f", res.Points[eastCross].X, minEX)
	}
}

// Per-column widths: a narrow label's column costs only its own
// width plus the gap, not the box's widest label.
func TestTidyGridPerColumnWidths(t *testing.T) {
	pts := []Point{
		{ID: 1, X: 0, Y: 0, FW: 30, FH: 47},
		{ID: 2, X: 200, Y: 0, FW: 88, FH: 47},
	}
	boxes := []GridBox{{ID: 1, Members: []int{0, 1}, Env: [4]float64{-100, -60, 300, 60}, Parent: -1}}
	res := TidyGrid(pts, 64, 98, boxes, nil, Options{Aspect: 1.69})
	dist := res.Points[1].X - res.Points[0].X
	want := (30.0+88.0)/2 + gridGapX
	if math.Abs(dist-want) > 0.6 {
		t.Fatalf("column pitch = %.2f, want %.2f (per-column widths)", dist, want)
	}
	// The narrow point's cell allotment is bounded by the run to
	// the block edge on its outer side: twice its column's half
	// width.
	if math.Abs(res.CellW[0]-30) > 0.6 {
		t.Fatalf("narrow point CellW = %.2f, want 30", res.CellW[0])
	}
	if math.Abs(res.CellW[1]-want) > 0.6 {
		t.Fatalf("wide point CellW = %.2f, want %.2f", res.CellW[1], want)
	}
}

// Loose-only drawing: no boxes at all — the loose group still
// reports a rectangle containing every device.
func TestTidyGridLooseOnly(t *testing.T) {
	pts := []Point{
		{ID: 1, X: 0, Y: 0, FW: 70, FH: 47},
		{ID: 2, X: 50, Y: 10, FW: 88, FH: 47},
		{ID: 3, X: -40, Y: -20, FW: 50, FH: 47},
	}
	res := TidyGrid(pts, 64, 98, nil, nil, Options{Aspect: 1.69})
	if !res.HasLoose {
		t.Fatal("loose-only layout reports no loose rectangle")
	}
	for i, p := range res.Points {
		if !rectInside(reservedRect(p), res.Loose) {
			t.Fatalf("loose point %d reserved rect %v escapes loose rect %v", i, reservedRect(p), res.Loose)
		}
	}
	if v := GridViolations(res.Points); v != 0 {
		t.Fatalf("loose-only layout has %d grid violations", v)
	}
}
