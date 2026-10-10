package layout

import (
	"math"
	"testing"
)

// Grouping preservation (v3.5.0): the stored squares on a map are
// semantic groups — carrier columns, bandwidth tiers, institute
// boxes — so the grid tidy must keep every box, keep nested boxes
// nested, and keep the stored left-to-right and top-to-bottom
// order of sibling boxes. The web client tries several layout
// variants per map (three design scales, column optimisation on
// and off, compact and full label footprints) and keeps the best
// fit, so the contract is checked across that whole matrix on
// synthetic fixtures at production scale (all ten maps that
// carry devices): no grid violations, every stored box
// drawn, sibling centre order unchanged on both axes, sibling
// boxes never overlapping, children inside their parent, every
// reserved rectangle inside its owner's drawn rectangle. Link
// crossings are reported for the primary variant of each map.

func segCross(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	d1 := (dx-cx)*(ay-cy) - (dy-cy)*(ax-cx)
	d2 := (dx-cx)*(by-cy) - (dy-cy)*(bx-cx)
	d3 := (bx-ax)*(cy-ay) - (by-ay)*(cx-ax)
	d4 := (bx-ax)*(dy-ay) - (by-ay)*(dx-ax)
	return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))
}

func countLinkCrossings(pts []Point, links [][2]int) int {
	n := 0
	for i := 0; i < len(links); i++ {
		for j := i + 1; j < len(links); j++ {
			a, b := links[i], links[j]
			if a[0] == b[0] || a[0] == b[1] || a[1] == b[0] || a[1] == b[1] {
				continue
			}
			pa, pb := pts[a[0]], pts[a[1]]
			pc, pd := pts[b[0]], pts[b[1]]
			if segCross(pa.X, pa.Y, pb.X, pb.Y, pc.X, pc.Y, pd.X, pd.Y) {
				n++
			}
		}
	}
	return n
}

func checkGrouping(t *testing.T, fx allMapsFixture, pts []Point, boxes []GridBox, links [][]int, opts Options, tag string) GridResult {
	t.Helper()
	res := TidyGrid(pts, 64, 98, boxes, links, opts)
	if v := GridViolations(res.Points); v != 0 {
		t.Errorf("%s: grid violations: %d", tag, v)
	}
	if c := linkCrossCount(res.Points, links); c != 0 {
		t.Errorf("%s: link crossings: %d", tag, c)
	}
	if len(res.Boxes) != len(boxes) {
		t.Fatalf("%s: drawn boxes %d, stored boxes %d", tag, len(res.Boxes), len(boxes))
	}
	centre := func(e [4]float64) (float64, float64) {
		return (e[0] + e[2]) / 2, (e[1] + e[3]) / 2
	}
	groups := map[int][]int{}
	for i, b := range boxes {
		groups[b.Parent] = append(groups[b.Parent], i)
	}
	for parent, idxs := range groups {
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				i, j := idxs[a], idxs[b]
				sxi, syi := centre(boxes[i].Env)
				sxj, syj := centre(boxes[j].Env)
				dxi, dyi := centre(res.Boxes[i])
				dxj, dyj := centre(res.Boxes[j])
				if sxj-sxi > 1e-9 && dxj-dxi < -0.5 {
					t.Errorf("%s: box %d moved right of box %d (parent %d): stored dx %.1f, drawn dx %.1f",
						tag, boxes[i].ID, boxes[j].ID, parent, sxj-sxi, dxj-dxi)
				}
				if sxj-sxi < -1e-9 && dxj-dxi > 0.5 {
					t.Errorf("%s: box %d moved left of box %d (parent %d): stored dx %.1f, drawn dx %.1f",
						tag, boxes[i].ID, boxes[j].ID, parent, sxj-sxi, dxj-dxi)
				}
				if syj-syi > 1e-9 && dyj-dyi < -0.5 {
					t.Errorf("%s: box %d moved above box %d (parent %d): stored dy %.1f, drawn dy %.1f",
						tag, boxes[i].ID, boxes[j].ID, parent, syj-syi, dyj-dyi)
				}
				if syj-syi < -1e-9 && dyj-dyi > 0.5 {
					t.Errorf("%s: box %d moved below box %d (parent %d): stored dy %.1f, drawn dy %.1f",
						tag, boxes[i].ID, boxes[j].ID, parent, syj-syi, dyj-dyi)
				}
				ri, rj := res.Boxes[i], res.Boxes[j]
				ox := math.Min(ri[2], rj[2]) - math.Max(ri[0], rj[0])
				oy := math.Min(ri[3], rj[3]) - math.Max(ri[1], rj[1])
				if ox > 0.5 && oy > 0.5 {
					t.Errorf("%s: sibling boxes %d and %d overlap by %.1f x %.1f",
						tag, boxes[i].ID, boxes[j].ID, ox, oy)
				}
			}
		}
		if parent >= 0 {
			p := res.Boxes[parent]
			for _, i := range idxs {
				c := res.Boxes[i]
				if c[0] < p[0]-0.5 || c[1] < p[1]-0.5 || c[2] > p[2]+0.5 || c[3] > p[3]+0.5 {
					t.Errorf("%s: box %d escapes parent box %d", tag, boxes[i].ID, boxes[parent].ID)
				}
			}
		}
	}
	owner := make([]int, len(pts))
	for i := range owner {
		owner[i] = -1
	}
	for bi, b := range boxes {
		for _, mi := range b.Members {
			owner[mi] = bi
		}
	}
	for i, p := range res.Points {
		if owner[i] >= 0 {
			if !rectInside(reservedRect(p), res.Boxes[owner[i]]) {
				t.Errorf("%s: point %s reserved rect %v escapes box %d rect %v",
					tag, fx.Names[i], reservedRect(p), boxes[owner[i]].ID, res.Boxes[owner[i]])
			}
		} else if res.HasLoose {
			if !rectInside(reservedRect(p), res.Loose) {
				t.Errorf("%s: loose point %s reserved rect %v escapes loose rect %v",
					tag, fx.Names[i], reservedRect(p), res.Loose)
			}
		}
	}
	return res
}

func scaledFixture(fx allMapsFixture, ptsIn []Point, f float64) ([]Point, []GridBox) {
	pts := make([]Point, len(ptsIn))
	for i, p := range ptsIn {
		pts[i] = p
		pts[i].X *= f
		pts[i].Y *= f
	}
	boxes := make([]GridBox, len(fx.Boxes))
	for i, b := range fx.Boxes {
		boxes[i] = b
		for k := 0; k < 4; k++ {
			boxes[i].Env[k] *= f
		}
	}
	return pts, boxes
}

func TestTidyGridAllMapsGroupingPreserved(t *testing.T) {
	const aspect = 1524.0 / 687.0
	for _, fx := range allMapsFixtures() {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			links := make([][]int, len(fx.Links))
			for i, lk := range fx.Links {
				links[i] = []int{lk[0], lk[1]}
			}
			variants := []struct {
				tag    string
				pts    []Point
				f      float64
				canvas bool
			}{}
			for _, v := range []struct {
				tag string
				pts []Point
			}{{"compact", fx.Pts}, {"full", fx.PtsF}} {
				for _, f := range []float64{1, 0.65, 0.42} {
					for _, canvas := range []bool{true, false} {
						variants = append(variants, struct {
							tag    string
							pts    []Point
							f      float64
							canvas bool
						}{v.tag, v.pts, f, canvas})
					}
				}
			}
			for _, v := range variants {
				pts, boxes := scaledFixture(fx, v.pts, v.f)
				opts := Options{Aspect: aspect}
				if v.canvas {
					opts.BoxW, opts.BoxH = 1524, 687
				}
				tag := v.tag
				if v.f != 1 {
					tag += " dk"
				}
				if !v.canvas {
					tag += " nocanvas"
				}
				checkGrouping(t, fx, pts, boxes, links, opts, tag)
			}
		})
	}
}
