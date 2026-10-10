package layout

import (
	"math"
	"testing"
)

// clusteredFixture is a deliberately nasty input: twelve nodes
// piled within a 10-unit patch (far tighter than the 60 x 100
// footprint the tidy view asks for), plus two nodes already well
// placed that should barely move.
func clusteredFixture() []Point {
	pts := []Point{}
	for i := 0; i < 12; i++ {
		pts = append(pts, Point{ID: int64(i + 1), X: 100 + float64(i%4)*3, Y: 200 + float64(i/4)*3})
	}
	pts = append(pts,
		Point{ID: 90, X: -500, Y: -400},
		Point{ID: 91, X: 900, Y: 800},
	)
	return pts
}

func TestTidyRespectsMinDistanceOnCluster(t *testing.T) {
	in := clusteredFixture()
	out := Tidy(in, 60, 100)
	if len(out) != len(in) {
		t.Fatalf("got %d points, want %d", len(out), len(in))
	}
	for i := range out {
		if out[i].ID != in[i].ID {
			t.Fatalf("point %d: ID changed from %d to %d", i, in[i].ID, out[i].ID)
		}
	}
	if v := Violations(out, 60-0.5, 100-0.5); v != 0 {
		t.Fatalf("tidy output still has %d violating pairs (0.5 unit tolerance)", v)
	}
}

func TestTidyDeterministic(t *testing.T) {
	in := clusteredFixture()
	a := Tidy(in, 60, 100)
	b := Tidy(in, 60, 100)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("run-to-run difference at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestTidyStaysRecognisable(t *testing.T) {
	in := clusteredFixture()
	out := Tidy(in, 60, 100)
	// The two already-isolated nodes must stay near their original
	// positions (the anchor spring holds the map's shape).
	for _, idx := range []int{12, 13} {
		d := math.Hypot(out[idx].X-in[idx].X, out[idx].Y-in[idx].Y)
		if d > 40 {
			t.Fatalf("isolated node %d drifted %.1f units, want <= 40", in[idx].ID, d)
		}
	}
	// The cluster's centroid must not fly away either.
	var ci, co [2]float64
	for i := 0; i < 12; i++ {
		ci[0] += in[i].X
		ci[1] += in[i].Y
		co[0] += out[i].X
		co[1] += out[i].Y
	}
	d := math.Hypot((co[0]-ci[0])/12, (co[1]-ci[1])/12)
	if d > 5 {
		t.Fatalf("cluster centroid moved %.2f units, want <= 5", d)
	}
}

func TestTidyCoincidentPoints(t *testing.T) {
	// Every node at exactly the same spot: the index-parity tie
	// break must still spread them deterministically.
	in := []Point{}
	for i := 0; i < 6; i++ {
		in = append(in, Point{ID: int64(i + 1), X: 50, Y: 50})
	}
	out := Tidy(in, 60, 100)
	if v := Violations(out, 60-0.5, 100-0.5); v != 0 {
		t.Fatalf("coincident input: %d violating pairs remain", v)
	}
	again := Tidy(in, 60, 100)
	for i := range out {
		if out[i] != again[i] {
			t.Fatalf("coincident input not deterministic at %d", i)
		}
	}
}

func TestSnapGroupsClustersNearValues(t *testing.T) {
	in := []Point{
		{ID: 1, X: 0, Y: 10},
		{ID: 2, X: 2, Y: 12},
		{ID: 3, X: 50, Y: 60},
		{ID: 4, X: 53, Y: 60},
		{ID: 5, X: 200, Y: 300},
	}
	out := SnapGroups(in, [][]int{{0, 1, 2, 3, 4}}, 5, 5)
	if out[0].X != out[1].X {
		t.Fatalf("X cluster {0,2} not snapped together: %v vs %v", out[0].X, out[1].X)
	}
	if out[2].X != out[3].X {
		t.Fatalf("X cluster {50,53} not snapped together: %v vs %v", out[2].X, out[3].X)
	}
	if out[4].X != 200 {
		t.Fatalf("isolated value moved: %v", out[4].X)
	}
	// Snapping never moves a point further than the tolerance.
	for i := range in {
		if math.Abs(out[i].X-in[i].X) > 5 || math.Abs(out[i].Y-in[i].Y) > 5 {
			t.Fatalf("point %d moved too far: %+v -> %+v", i, in[i], out[i])
		}
	}
	// The input slice is not modified.
	if in[1].X != 2 {
		t.Fatalf("input mutated: %+v", in[1])
	}
}

func TestSnapGroupsRespectsGroupBoundaries(t *testing.T) {
	// Two devices in DIFFERENT groups at nearly the same X must
	// not be snapped onto one line — their boxes are separate.
	in := []Point{
		{ID: 1, X: 100, Y: 0},
		{ID: 2, X: 103, Y: 500},
	}
	out := SnapGroups(in, [][]int{{0}, {1}}, 10, 10)
	if out[0].X != 100 || out[1].X != 103 {
		t.Fatalf("cross-group snap happened: %+v", out)
	}
}

func TestTidySnapStraightensJitteredGrid(t *testing.T) {
	// A 3x3 grid whose columns are jittered by a few units: the
	// relaxation alone leaves up to 9 distinct X values; the snap
	// pass must bring the grid back to 3 columns and 3 rows while
	// keeping every pair separated.
	pts := []Point{}
	id := int64(1)
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			jx := float64((int(id)*7)%5) - 2 // -2..+2 deterministic jitter
			jy := float64((int(id)*3)%5) - 2
			pts = append(pts, Point{ID: id, X: float64(c)*110 + jx, Y: float64(r)*130 + jy})
			id++
		}
	}
	out := TidySnap(pts, 64, 106, [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8}}, 30, 30)
	distinct := map[float64]bool{}
	distinctY := map[float64]bool{}
	for _, p := range out {
		distinct[math.Round(p.X*2)/2] = true
		distinctY[math.Round(p.Y*2)/2] = true
	}
	if len(distinct) > 3 {
		t.Fatalf("after snap: %d distinct X values, want <= 3", len(distinct))
	}
	if len(distinctY) > 3 {
		t.Fatalf("after snap: %d distinct Y values, want <= 3", len(distinctY))
	}
	if v := Violations(out, 64-0.5, 106-0.5); v != 0 {
		t.Fatalf("snap left %d violating pairs", v)
	}
	again := TidySnap(pts, 64, 106, [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8}}, 30, 30)
	for i := range out {
		if out[i] != again[i] {
			t.Fatalf("TidySnap not deterministic at %d", i)
		}
	}
}

func TestTidySnapLeavesSeparatedInputAlone(t *testing.T) {
	// Well-separated points far from any shared line: nothing may
	// move (the snap tolerance is far below their spacing).
	in := []Point{
		{ID: 1, X: 0, Y: 0},
		{ID: 2, X: 400, Y: 90},
		{ID: 3, X: 130, Y: 700},
	}
	out := TidySnap(in, 64, 106, nil, 20, 20)
	maxMove := 0.0
	for i := range in {
		maxMove = math.Max(maxMove, math.Abs(out[i].X-in[i].X)+math.Abs(out[i].Y-in[i].Y))
	}
	if maxMove > 40 {
		t.Fatalf("separated input moved %.1f units", maxMove)
	}
}

func TestTidyFootprintsReserveLabelSpace(t *testing.T) {
	// Two nodes at the same spot, each carrying a wide, tall
	// footprint (marker + label stack), under small call-level
	// minima: the arrangement must still separate them by the
	// footprint, not by the small minima.
	in := []Point{
		{ID: 1, X: 100, Y: 100, FW: 200, FH: 160},
		{ID: 2, X: 100, Y: 100, FW: 200, FH: 160},
		{ID: 3, X: 104, Y: 98, FW: 200, FH: 160},
	}
	out := Tidy(in, 10, 10)
	if v := Violations(out, 10-0.5, 10-0.5); v != 0 {
		t.Fatalf("footprint output still has %d violating pairs", v)
	}
	// Explicitly: every pair clears the footprint on some axis.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			dx := math.Abs(out[i].X - out[j].X)
			dy := math.Abs(out[i].Y - out[j].Y)
			if dx < 200-0.5 && dy < 160-0.5 {
				t.Fatalf("pair %d,%d inside footprint: dx=%.1f dy=%.1f", i, j, dx, dy)
			}
		}
	}
}

func TestTidyFootprintsDeterministicAndSnapped(t *testing.T) {
	// A jittered column of footprinted nodes through the full
	// TidySnap pipeline: the snap must not re-create a footprint
	// violation (the settle pass runs with the footprints too).
	pts := []Point{}
	for i := 0; i < 6; i++ {
		pts = append(pts, Point{ID: int64(i + 1), X: 100 + float64(i%2)*3, Y: 100 + float64(i)*40, FW: 150, FH: 150})
	}
	grp := [][]int{{0, 1, 2, 3, 4, 5}}
	a := TidySnap(pts, 64, 106, grp, 30, 30)
	b := TidySnap(pts, 64, 106, grp, 30, 30)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("TidySnap with footprints not deterministic at %d", i)
		}
	}
	if v := Violations(a, 64-0.5, 106-0.5); v != 0 {
		t.Fatalf("snap+settle with footprints left %d violating pairs", v)
	}
}

func TestTidyMixedFootprintsFallBackToMinima(t *testing.T) {
	// A point without a footprint pairs against the call-level
	// minima only — older callers are unaffected.
	in := []Point{
		{ID: 1, X: 0, Y: 0, FW: 300, FH: 300},
		{ID: 2, X: 30, Y: 0},
	}
	out := Tidy(in, 60, 100)
	if v := Violations(out, 60-0.5, 100-0.5); v != 0 {
		t.Fatalf("mixed footprints: %d violating pairs", v)
	}
	dx := math.Abs(out[0].X - out[1].X)
	dy := math.Abs(out[0].Y - out[1].Y)
	if dx >= 299 || dy >= 299 {
		t.Fatalf("unfootprinted pair was separated by the other's footprint: dx=%.1f dy=%.1f", dx, dy)
	}
}

func TestTidyDegenerateInputs(t *testing.T) {
	if got := Tidy(nil, 60, 100); len(got) != 0 {
		t.Fatalf("nil input: got %d points", len(got))
	}
	one := []Point{{ID: 1, X: 3, Y: 4}}
	if got := Tidy(one, 60, 100); len(got) != 1 || got[0] != one[0] {
		t.Fatalf("single point changed: %+v", got)
	}
	// A non-positive minimum disables tidying rather than dividing
	// by nonsense.
	two := []Point{{ID: 1}, {ID: 2}}
	if got := Tidy(two, 0, 100); got[0] != two[0] || got[1] != two[1] {
		t.Fatalf("zero minDX should return the input unchanged: %+v", got)
	}
}

// A dense cluster whose stored span is far smaller than its
// combined footprints cannot be repacked by pairwise nudges
// alone; with a canvas box the layout must fall back to shelf
// packing and return an arrangement that fits the box with no
// footprint violations.
func TestTidySnapBoxPacksDenseCluster(t *testing.T) {
	var pts []Point
	for i := 0; i < 60; i++ {
		pts = append(pts, Point{ID: int64(i + 1), X: float64(100 + (i%6)*8), Y: float64(100 + (i/6)*8), FW: 130, FH: 90})
	}
	out := TidySnapOpts(pts, 64, 106, [][]int{seq(len(pts))}, 30, 30, Options{Aspect: 1278.0 / 756.0, BoxW: 1278, BoxH: 756})
	if v := Violations(out, 64, 106); v != 0 {
		t.Fatalf("packed arrangement has %d footprint violations", v)
	}
	w, h := spanOf(out)
	if w > 1278*1.15 || h > 756*1.15 {
		t.Fatalf("packed span %.0fx%.0f overflows the 1278x756 box", w, h)
	}
}

func seq(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

// scatteredChainFixture (v3.4.4) is a chain of eight linked nodes
// whose stored positions are deliberately scattered across a wide
// field in scrambled order: every link starts out spanning most
// of the drawing. Footprints match the tidy view's screen-space
// points (marker plus label stack).
func scatteredChainFixture() ([]Point, [][]int) {
	pos := [][2]float64{
		{0, 1500}, {1900, 100}, {300, 300}, {1600, 1400},
		{700, 800}, {1200, 200}, {100, 900}, {1800, 900},
	}
	pts := make([]Point, len(pos))
	for i, p := range pos {
		pts[i] = Point{ID: int64(i + 1), X: p[0], Y: p[1], FW: 90, FH: 110}
	}
	var links [][]int
	for i := 0; i+1 < len(pts); i++ {
		links = append(links, []int{i, i + 1})
	}
	return pts, links
}

func meanLinkLen(pts []Point, links [][]int) float64 {
	if len(links) == 0 {
		return 0
	}
	sum := 0.0
	for _, lk := range links {
		sum += math.Hypot(pts[lk[1]].X-pts[lk[0]].X, pts[lk[1]].Y-pts[lk[0]].Y)
	}
	return sum / float64(len(links))
}

func TestTidyLinksPullPartnersTogether(t *testing.T) {
	pts, links := scatteredChainFixture()
	before := meanLinkLen(pts, links)
	out := TidyOpts(pts, 64, 98, Options{Links: links})
	after := meanLinkLen(out, links)
	if after > 0.45*before {
		t.Fatalf("mean link length %.1f -> %.1f; linked partners should end up adjacent", before, after)
	}
	for _, lk := range links {
		d := math.Hypot(out[lk[1]].X-out[lk[0]].X, out[lk[1]].Y-out[lk[0]].Y)
		// A chain folded into a two-dimensional pack cannot put
		// every hop at one pitch; the worst fold-back hop lands
		// around two and a half pitches. Anything near the
		// original span (thousands of units) is a failure.
		if d > 300 {
			t.Fatalf("link %v still spans %.1f units; want partners within roughly two label pitches", lk, d)
		}
	}
	if v := Violations(out, 64-0.5, 98-0.5); v != 0 {
		t.Fatalf("link-aware tidy left %d violating pairs", v)
	}
	again := TidyOpts(pts, 64, 98, Options{Links: links})
	for i := range out {
		if out[i] != again[i] {
			t.Fatalf("link-aware tidy not deterministic at %d", i)
		}
	}
}

func TestTidySnapLinksCrossGroup(t *testing.T) {
	// Two clusters far apart; a chain inside each, one link
	// across. The cross pair must migrate toward each other
	// (facing edges) instead of keeping the full span.
	pts := []Point{
		{ID: 1, X: 0, Y: 0, FW: 90, FH: 110},
		{ID: 2, X: 30, Y: 10, FW: 90, FH: 110},
		{ID: 3, X: 10, Y: 40, FW: 90, FH: 110},
		{ID: 4, X: 1500, Y: 900, FW: 90, FH: 110},
		{ID: 5, X: 1530, Y: 910, FW: 90, FH: 110},
		{ID: 6, X: 1510, Y: 940, FW: 90, FH: 110},
	}
	links := [][]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 5}}
	groups := [][]int{{0, 1, 2}, {3, 4, 5}}
	crossBefore := math.Hypot(pts[3].X-pts[2].X, pts[3].Y-pts[2].Y)
	out := TidySnapOpts(pts, 64, 98, groups, 30, 30, Options{Links: links})
	crossAfter := math.Hypot(out[3].X-out[2].X, out[3].Y-out[2].Y)
	if crossAfter > 0.5*crossBefore {
		t.Fatalf("cross-group link %.1f -> %.1f; partners should meet at facing edges", crossBefore, crossAfter)
	}
	if v := Violations(out, 64-0.5, 98-0.5); v != 0 {
		t.Fatalf("cross-group tidy left %d violating pairs", v)
	}
}

func TestTidyInvalidLinksIgnored(t *testing.T) {
	pts, _ := scatteredChainFixture()
	out := TidyOpts(pts, 64, 98, Options{Links: [][]int{{-1, 3}, {2, 99}, {4, 4}, {1}}})
	if len(out) != len(pts) {
		t.Fatalf("got %d points, want %d", len(out), len(pts))
	}
	if v := Violations(out, 64-0.5, 98-0.5); v != 0 {
		t.Fatalf("invalid-link tidy left %d violating pairs", v)
	}
}
