package layout

import (
	"math"
	"testing"
)

// v3.6 link-length pass: a diagonal link across a 2x2 grid must be
// shortened by swapping its far endpoint with the nearer device in
// the same box, without creating crossings or moving any device
// out of its box.
func TestShortenLinksSwapsToShorten(t *testing.T) {
	pts := []Point{
		{ID: 1, X: 0, Y: 100, FW: 40, FH: 60},
		{ID: 2, X: 100, Y: 100, FW: 40, FH: 60},
		{ID: 3, X: 0, Y: 0, FW: 40, FH: 60},
		{ID: 4, X: 100, Y: 0, FW: 40, FH: 60},
	}
	links := [][]int{{0, 3}} // the diagonal: length ~141
	owner := []int{0, 0, 0, 0}
	boxes := [][4]float64{{-60, -70, 160, 160}}
	cellW := []float64{60, 60, 60, 60}

	before := math.Hypot(pts[0].X-pts[3].X, pts[0].Y-pts[3].Y)
	shortenLinks(pts, cellW, links, owner, boxes, [4]float64{}, false)
	after := math.Hypot(pts[0].X-pts[3].X, pts[0].Y-pts[3].Y)
	if after >= before {
		t.Errorf("link length before %.1f after %.1f — the swap pass did not shorten it", before, after)
	}
	if after > 100.5 {
		t.Errorf("link length after = %.1f, want <= 100 (an edge of the grid)", after)
	}
	if c := linkCrossCount(pts, links); c != 0 {
		t.Errorf("crossings after = %d, want 0", c)
	}
	for i, p := range pts {
		if p.X-p.FW/2 < boxes[0][0]-0.5 || p.X+p.FW/2 > boxes[0][2]+0.5 ||
			p.Y-p.FH < boxes[0][1]-0.5 || p.Y+gridMarkerTop > boxes[0][3]+0.5 {
			t.Errorf("point %d left its box: %+v", i, p)
		}
	}
}

// An already-short arrangement must not be made worse: the pass
// accepts only strict improvements of (max, total).
func TestShortenLinksKeepsGoodArrangement(t *testing.T) {
	pts := []Point{
		{ID: 1, X: 0, Y: 100, FW: 40, FH: 60},
		{ID: 2, X: 100, Y: 100, FW: 40, FH: 60},
		{ID: 3, X: 0, Y: 0, FW: 40, FH: 60},
		{ID: 4, X: 100, Y: 0, FW: 40, FH: 60},
	}
	links := [][]int{{0, 1}, {2, 3}} // two short horizontal links
	owner := []int{0, 0, 0, 0}
	boxes := [][4]float64{{-60, -70, 160, 160}}
	cellW := []float64{60, 60, 60, 60}
	orig := make([]Point, len(pts))
	copy(orig, pts)
	shortenLinks(pts, cellW, links, owner, boxes, [4]float64{}, false)
	for i := range pts {
		if pts[i].X != orig[i].X || pts[i].Y != orig[i].Y {
			t.Errorf("point %d moved from (%v,%v) to (%v,%v) although the arrangement was already optimal",
				i, orig[i].X, orig[i].Y, pts[i].X, pts[i].Y)
		}
	}
}
