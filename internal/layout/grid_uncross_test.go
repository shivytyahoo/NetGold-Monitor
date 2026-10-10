package layout

import (
	"math"
	"testing"
)

// Uncrossing (v3.5.0): links that join the same pair of boxes
// must not cross in the space between them. The first test
// mirrors a reported production case — two links between one
// sub-box and the main box of the INTER-ZONE-100M fixture map
// whose endpoint order along the facing sides disagreed. The
// second is a hand-built synthetic case proving the pass itself
// is data-driven, not tuned to that map.

func TestTidyGridUncrossingInterZone(t *testing.T) {
	pts, boxes := synthFixtureInterZone()
	links := [][]int{{8, 9}, {16, 17}}
	res := TidyGrid(pts, 64, 98, boxes, links,
		Options{Aspect: 1278.0 / 756.0, BoxW: 1278, BoxH: 756})
	if c := linkCrossCount(res.Points, links); c != 0 {
		t.Fatalf("link crossings after uncrossing: %d", c)
	}
	if v := GridViolations(res.Points); v != 0 {
		t.Fatalf("grid violations after uncrossing: %d", v)
	}
	// The linked pair stays a short hop (the facing guarantee).
	d := math.Hypot(res.Points[8].X-res.Points[9].X, res.Points[8].Y-res.Points[9].Y)
	if d >= 400 {
		t.Fatalf("cross-box pair length %.0f, want < 400", d)
	}
	// Every reserved rectangle still inside its owner's rect.
	owner := make([]int, len(pts))
	for i := range owner {
		owner[i] = -1
	}
	for bi, b := range boxes {
		for _, m := range b.Members {
			owner[m] = bi
		}
	}
	for i, p := range res.Points {
		if owner[i] >= 0 && !rectInside(reservedRect(p), res.Boxes[owner[i]]) {
			t.Fatalf("point %d reserved rect %v escapes box %d rect %v after uncrossing",
				i, reservedRect(p), boxes[owner[i]].ID, res.Boxes[owner[i]])
		}
	}
}

func TestUncrossLinksSynthetic(t *testing.T) {
	// Two boxes side by side; the links are crossed by
	// construction: top-left to bottom-right and bottom-left to
	// top-right.
	pts := []Point{
		{ID: 1, X: 50, Y: 150, FW: 60, FH: 47},
		{ID: 2, X: 50, Y: 50, FW: 60, FH: 47},
		{ID: 3, X: 350, Y: 50, FW: 60, FH: 47},
		{ID: 4, X: 350, Y: 150, FW: 60, FH: 47},
	}
	links := [][]int{{0, 2}, {1, 3}}
	ownerOf := []int{0, 0, 1, 1}
	boxRects := [][4]float64{{0, 0, 200, 220}, {300, 0, 500, 220}}
	cellW := []float64{100, 100, 100, 100}
	if c := linkCrossCount(pts, links); c != 1 {
		t.Fatalf("setup: crossings = %d, want 1", c)
	}
	uncrossLinks(pts, cellW, links, ownerOf, boxRects, [4]float64{}, false)
	if c := linkCrossCount(pts, links); c != 0 {
		t.Fatalf("crossings after uncrossing: %d", c)
	}
	// Devices stayed in their own box (x ranges are the boxes').
	if pts[0].X > 200 || pts[1].X > 200 || pts[2].X < 300 || pts[3].X < 300 {
		t.Fatalf("a device left its box: %v", pts)
	}
	if v := GridViolations(pts); v != 0 {
		t.Fatalf("grid violations after uncrossing: %d", v)
	}
}
