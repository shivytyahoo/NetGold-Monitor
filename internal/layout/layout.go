// Package layout computes the "tidy view" arrangement for diagram
// pages (v3.4). The stored positions of an imported legacy map
// (or a hand-built topology) are the faithful record and are never
// modified; the tidy view is a computed arrangement on top of them:
// every node is nudged just far enough that no two nodes sit closer
// than the marker-plus-label footprint, while a gentle pull toward
// the original position keeps the drawing recognisable.
//
// The algorithm is an iterative relaxation over the node list:
// repeated pairwise separation (project each overlapping pair apart
// along its cheaper axis) with a weak anchor spring toward the
// original positions. It is fully deterministic — fixed iteration
// counts, pairs visited in slice order, ties broken by index — so
// the same input always produces the same output.
//
// TidySnap adds a grid-snap pass (v3.4.2) between the relaxation and
// the final settle: coordinates that lie within a small tolerance
// of each other (inside a group of nodes that belong together, such
// as the devices of one map region) are pulled onto one shared
// line, so rows and columns come out ruler-straight, and the settle
// pass that follows guarantees the snap cannot leave two nodes
// closer than the minima.
//
// Points may also carry an individual footprint (v3.4.3): the
// rectangle each node must keep clear around its centre — marker
// plus the label stack the renderer will draw beside it. When both
// points of a pair carry a footprint on an axis, the pair's
// minimum centre distance on that axis rises to the average of
// the two footprints, so a node's label space is reserved by the
// arrangement itself instead of being left for the label planner
// to fight over. Points without a footprint fall back to the
// call-level minima, so older callers see exactly the old
// behaviour.
//
// The layout is link-aware (v3.4.4): when the caller supplies the
// drawing's link pairs, springs between linked points pull
// partners toward one label pitch of each other during the
// relaxation, and a final polish pass — link springs against a
// very weak anchor, each round followed by separation
// projections — settles connected devices side by side instead
// of leaving them wherever the stored drawing happened to put
// them, with lines spanning the whole map. The shelf-packed seed
// likewise orders each group by a breadth-first walk over the
// link graph and packs groups with the most links between them
// next to each other, so packed drawings keep partners — and
// linked region boxes — adjacent as well. Callers that supply
// no links see exactly the pre-v3.4.4 behaviour.
package layout

import (
	"math"
	"sort"
)

// Point is one node's position. X grows right, Y grows up (the
// the legacy system map convention); the separation test is symmetric,
// so the axis orientation does not affect the result. FW and FH
// are the node's optional reserved footprint (full width and
// height, in the same units as X and Y); zero means no individual
// footprint.
type Point struct {
	ID int64   `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	FW float64 `json:"fw,omitempty"`
	FH float64 `json:"fh,omitempty"`
}

// pairMinima returns the minimum centre distances for one pair:
// the call-level minima, raised by the pair's footprints when
// both points carry one on that axis.
func pairMinima(a, b Point, minDX, minDY float64) (float64, float64) {
	dx, dy := minDX, minDY
	if a.FW > 0 && b.FW > 0 {
		if v := (a.FW + b.FW) / 2; v > dx {
			dx = v
		}
	}
	if a.FH > 0 && b.FH > 0 {
		if v := (a.FH + b.FH) / 2; v > dy {
			dy = v
		}
	}
	return dx, dy
}

// separation reports how far apart two centres are along each axis
// relative to the required minima. A pair is separated when either
// axis meets its minimum (rectangular footprint: a label stack
// hangs below its marker, so two nodes in the same column need
// vertical room, while two nodes side by side at the same height
// need horizontal room).
func separated(ax, ay, bx, by, minDX, minDY float64) bool {
	return math.Abs(ax-bx) >= minDX || math.Abs(ay-by) >= minDY
}

// separatedPair is separated with the pair's own minima (the
// call-level minima raised by the pair's footprints).
func separatedPair(a, b Point, minDX, minDY float64) bool {
	dx, dy := pairMinima(a, b, minDX, minDY)
	return separated(a.X, a.Y, b.X, b.Y, dx, dy)
}

// axisBias returns the push-axis preference multiplier for the
// current span of out relative to a target aspect (width /
// height): when the drawing is taller than the target, the
// multiplier rises above 1 so the pusher prices vertical moves
// up and pairs separate sideways more often, and vice versa.
// The feedback is self-correcting — as the span approaches the
// target aspect the multiplier returns to 1 (no preference).
// A zero target aspect disables the bias.
func axisBias(out []Point, targetAspect float64) float64 {
	if targetAspect <= 0 || len(out) == 0 {
		return 1
	}
	minX, maxX, minY, maxY := out[0].X, out[0].X, out[0].Y, out[0].Y
	for _, p := range out[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	w, h := maxX-minX, maxY-minY
	if w <= 0 || h <= 0 {
		return 1
	}
	m := targetAspect * h / w
	if m < 0.4 {
		m = 0.4
	}
	if m > 2.5 {
		m = 2.5
	}
	return m
}

// pusher returns the pairwise separation step over out: the two
// centres of a violating pair move apart along the axis that needs
// the smaller move, half each. bias prices the vertical move (see
// axisBias): the pair separates along X when ox <= oy*bias.
// Identical positions (and exact axis ties) are broken by index
// parity so the result never depends on floating-point noise:
// the lower index moves in the negative direction.
func pusher(out []Point, minDX, minDY, bias float64) func(i, j int) {
	return func(i, j int) {
		pdx, pdy := pairMinima(out[i], out[j], minDX, minDY)
		dx := out[j].X - out[i].X
		dy := out[j].Y - out[i].Y
		ox := pdx - math.Abs(dx)
		oy := pdy - math.Abs(dy)
		if ox <= 0 || oy <= 0 {
			return // already separated on one axis
		}
		if ox <= oy*bias {
			half := ox / 2
			if dx > 0 {
				out[i].X -= half
				out[j].X += half
			} else if dx < 0 {
				out[i].X += half
				out[j].X -= half
			} else if i%2 == 0 {
				out[i].X -= half
				out[j].X += half
			} else {
				out[i].X += half
				out[j].X -= half
			}
		} else {
			half := oy / 2
			if dy > 0 {
				out[i].Y -= half
				out[j].Y += half
			} else if dy < 0 {
				out[i].Y += half
				out[j].Y -= half
			} else if i%2 == 0 {
				out[i].Y -= half
				out[j].Y += half
			} else {
				out[i].Y += half
				out[j].Y -= half
			}
		}
	}
}

// linkTarget returns the centre distance a link pulls its pair
// toward: a little over the average of the pair's two separation
// pitches, so linked points settle side by side (or stacked) at
// roughly one label pitch — adjacent, not on top of each other.
func linkTarget(a, b Point, minDX, minDY float64) float64 {
	tx, ty := pairMinima(a, b, minDX, minDY)
	return (tx + ty) * 0.55
}

// linkStep applies one spring step for every link: a pair farther
// apart than its target distance moves together, each endpoint
// covering half the step, so a link never drags the drawing's
// centroid. Pairs at or under the target are left alone — the
// separation projection owns the too-close side. Out-of-range
// and self links are ignored. Deterministic: links are visited
// in slice order.
func linkStep(out []Point, links [][]int, minDX, minDY, beta float64) {
	for _, lk := range links {
		if len(lk) != 2 {
			continue
		}
		i, j := lk[0], lk[1]
		if i < 0 || j < 0 || i >= len(out) || j >= len(out) || i == j {
			continue
		}
		dx := out[j].X - out[i].X
		dy := out[j].Y - out[i].Y
		d := math.Hypot(dx, dy)
		if d == 0 {
			continue
		}
		if t := linkTarget(out[i], out[j], minDX, minDY); d > t {
			m := (d - t) * beta / 2 / d
			out[i].X += dx * m
			out[i].Y += dy * m
			out[j].X -= dx * m
			out[j].Y -= dy * m
		}
	}
}

// relaxPhase runs the separation passes with the weak anchor
// spring, so the arrangement stays close to the drawing it came
// from.
func relaxPhase(out, orig []Point, minDX, minDY, aspect float64, links [][]int) {
	relaxPasses(out, orig, minDX, minDY, aspect, 160, links)
}

func relaxPasses(out, orig []Point, minDX, minDY, aspect float64, passes int, links [][]int) {
	relaxSpring(out, orig, minDX, minDY, aspect, passes, 0.012, links)
}

// relaxSpring runs separation passes with an anchor spring of
// the given strength: 1.2% of the way back to the original
// position per pass keeps a map recognisable while constraints
// win; the finish phase re-runs it much weaker when a dense
// packing is jammed, because there the spring itself is what
// re-creates the violations the settle just removed. When links
// are supplied, each pass also takes a gentle link-spring step,
// so partners drift together while the geography is still fluid.
func relaxSpring(out, orig []Point, minDX, minDY, aspect float64, passes int, spring float64, links [][]int) {
	for pass := 0; pass < passes; pass++ {
		push := pusher(out, minDX, minDY, axisBias(out, aspect))
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				push(i, j)
			}
		}
		for i := range out {
			out[i].X += (orig[i].X - out[i].X) * spring
			out[i].Y += (orig[i].Y - out[i].Y) * spring
		}
		if len(links) > 0 {
			linkStep(out, links, minDX, minDY, 0.05)
		}
	}
}

// validLinks filters a link list to well-formed pairs of
// in-range, distinct point indices.
func validLinks(n int, links [][]int) [][]int {
	var out [][]int
	for _, lk := range links {
		if len(lk) == 2 && lk[0] >= 0 && lk[1] >= 0 && lk[0] < n && lk[1] < n && lk[0] != lk[1] {
			out = append(out, lk)
		}
	}
	return out
}

// linkSwapPass improves a finished arrangement by permuting
// which node sits in which position (v3.4.4): for each linked
// node it tries swapping positions with its partners and with
// the nodes sitting near its partners, keeping a swap only when
// the total length of the affected links falls and no footprint
// violation is added. Permuting assignments is what frees a
// tight packing — springs alone cannot move a node through its
// neighbours. Swaps stay inside a group when groupOf is given
// (a node's region membership travels with the node, so a swap
// across regions would strand both nodes in each other's
// boxes); a nil groupOf treats every node as one group.
// Deterministic: nodes and candidates are visited in index
// order and the best improving swap wins, ties by lowest index.
func linkSwapPass(out []Point, minDX, minDY float64, links [][]int, groupOf []int) {
	if len(links) == 0 || len(out) < 2 {
		return
	}
	adj := linkAdjacency(len(out), links)
	sameGroup := func(a, b int) bool {
		return groupOf == nil || groupOf[a] == groupOf[b]
	}
	violTouching := func(u, v int) int {
		n := 0
		for w := 0; w < len(out); w++ {
			if w == u || w == v {
				continue
			}
			if !separatedPair(out[u], out[w], minDX, minDY) {
				n++
			}
			if !separatedPair(out[v], out[w], minDX, minDY) {
				n++
			}
		}
		return n
	}
	costTouching := func(u, v int) float64 {
		s := 0.0
		for _, lk := range links {
			if len(lk) != 2 {
				continue
			}
			if lk[0] == u || lk[1] == u || lk[0] == v || lk[1] == v {
				s += math.Hypot(out[lk[1]].X-out[lk[0]].X, out[lk[1]].Y-out[lk[0]].Y)
			}
		}
		return s
	}
	swapPos := func(u, v int) {
		out[u].X, out[v].X = out[v].X, out[u].X
		out[u].Y, out[v].Y = out[v].Y, out[u].Y
	}
	for sweep := 0; sweep < 60; sweep++ {
		swapped := false
		for u := 0; u < len(out); u++ {
			if len(adj[u]) == 0 {
				continue
			}
			candSet := map[int]bool{}
			for _, p := range adj[u] {
				candSet[p] = true
				reach := linkTarget(out[u], out[p], minDX, minDY) * 1.6
				for w := 0; w < len(out); w++ {
					if w != u && math.Hypot(out[w].X-out[p].X, out[w].Y-out[p].Y) <= reach {
						candSet[w] = true
					}
				}
			}
			var cands []int
			for w := range candSet {
				if w != u && sameGroup(u, w) {
					cands = append(cands, w)
				}
			}
			sort.Ints(cands)
			bestV, bestDelta := -1, -0.5
			for _, v := range cands {
				v0 := violTouching(u, v)
				c0 := costTouching(u, v)
				swapPos(u, v)
				d := costTouching(u, v) - c0
				ok := d < bestDelta && violTouching(u, v) <= v0
				swapPos(u, v)
				if ok {
					bestV, bestDelta = v, d
				}
			}
			if bestV >= 0 {
				swapPos(u, bestV)
				swapped = true
			}
		}
		if !swapped {
			break
		}
	}
}

// linkSwapRescue is the swap pass's heavy artillery (v3.4.4):
// for a link that is still far beyond its target after the
// strict pass — classically a pendant pair whose endpoints are
// boxed in by a dense packing, where every strict swap is
// refused because the displaced node does not fit the vacated
// slot — it tries swapping one endpoint with a node sitting
// near the other endpoint and lets the separation projection
// settle the result. An attempt is kept only when the settled
// arrangement has no more violations than before the attempt
// and a strictly lower total link cost, so the rescue can fix a
// stranded link but never degrade the drawing. Attempts work
// on the worst link first and stop at the first link that
// cannot be improved. Deterministic: candidates are tried in
// distance order with index tie-breaks, and all caps are fixed.
func linkSwapRescue(out []Point, minDX, minDY, aspect float64, links [][]int, groupOf []int) {
	if len(links) == 0 || len(out) < 2 {
		return
	}
	totalCost := func(ps []Point) float64 {
		s := 0.0
		for _, lk := range links {
			if len(lk) == 2 {
				s += math.Hypot(ps[lk[1]].X-ps[lk[0]].X, ps[lk[1]].Y-ps[lk[0]].Y)
			}
		}
		return s
	}
	sameGroup := func(a, b int) bool {
		return groupOf == nil || groupOf[a] == groupOf[b]
	}
	for attempt := 0; attempt < 8; attempt++ {
		wi, worst := -1, 2.2
		for li, lk := range links {
			if len(lk) != 2 {
				continue
			}
			d := math.Hypot(out[lk[1]].X-out[lk[0]].X, out[lk[1]].Y-out[lk[0]].Y)
			if t := linkTarget(out[lk[0]], out[lk[1]], minDX, minDY); t > 0 && d/t > worst {
				wi, worst = li, d/t
			}
		}
		if wi < 0 {
			return
		}
		lk := links[wi]
		baseViol := Violations(out, minDX, minDY)
		baseCost := totalCost(out)
		improved := false
		for _, ends := range [][2]int{{lk[0], lk[1]}, {lk[1], lk[0]}} {
			u, p := ends[0], ends[1]
			type cand struct {
				v int
				d float64
			}
			var cands []cand
			reach := linkTarget(out[u], out[p], minDX, minDY) * 2.0
			for w := 0; w < len(out); w++ {
				if w == u || w == p || !sameGroup(u, w) {
					continue
				}
				if d := math.Hypot(out[w].X-out[p].X, out[w].Y-out[p].Y); d <= reach {
					cands = append(cands, cand{w, d})
				}
			}
			if len(cands) == 0 {
				// Cross-group rescue: nobody in u's group sits
				// near the partner, so offer u's group-mates
				// nearest the partner — the swap moves u to
				// its group's facing edge and the settle does
				// the rest.
				for w := 0; w < len(out); w++ {
					if w == u || w == p || !sameGroup(u, w) {
						continue
					}
					cands = append(cands, cand{w, math.Hypot(out[w].X-out[p].X, out[w].Y-out[p].Y)})
				}
			}
			sort.Slice(cands, func(a, b int) bool {
				if cands[a].d != cands[b].d {
					return cands[a].d < cands[b].d
				}
				return cands[a].v < cands[b].v
			})
			if len(cands) > 5 {
				cands = cands[:5]
			}
			for _, cd := range cands {
				trial := make([]Point, len(out))
				copy(trial, out)
				trial[u].X, trial[cd.v].X = trial[cd.v].X, trial[u].X
				trial[u].Y, trial[cd.v].Y = trial[cd.v].Y, trial[u].Y
				settleBounded(trial, minDX, minDY, aspect, 80)
				if Violations(trial, minDX, minDY) <= baseViol && totalCost(trial) < baseCost-1 {
					copy(out, trial)
					improved = true
					break
				}
			}
			if improved {
				break
			}
		}
		if !improved {
			return
		}
	}
}

// settleBounded is settlePhase with a smaller pass cap, for
// trial arrangements that only need a local repair.
func settleBounded(out []Point, minDX, minDY, aspect float64, passes int) {
	for pass := 0; pass < passes; pass++ {
		push := pusher(out, minDX, minDY, axisBias(out, aspect))
		violations := 0
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				if !separatedPair(out[i], out[j], minDX, minDY) {
					violations++
					push(i, j)
				}
			}
		}
		if violations == 0 {
			break
		}
	}
}

// linkPolish is the link-aware finish (v3.4.4). It works in two
// guarded steps. First a swap pass permutes node assignments —
// the only move that can shorten links inside a tight packing.
// Then rounds of link springs against a very weak anchor to the
// swapped positions, each round followed by separation
// projections, pull partners the rest of the way together. The
// weak anchor bounds the drift — a link can pull its endpoints
// together, but the pair's midpoint stays near where the
// arrangement put it. Each step is kept only when it leaves no
// more footprint violations than the arrangement it started
// from, so the polish can improve links but never degrade the
// separation contract. groupOf (when given) confines swaps to
// nodes of one group; springs are not confined — a cross-group
// link pulls its endpoints to the facing edges of their groups.
func linkPolish(out []Point, minDX, minDY, aspect float64, links [][]int, groupOf []int) {
	// The endpoint handler sanitises link pairs, but the layout
	// stays defensive: only well-formed in-range pairs take part.
	links = validLinks(len(out), links)
	if len(links) == 0 || len(out) < 2 {
		return
	}
	totalCost := func(ps []Point) float64 {
		s := 0.0
		for _, lk := range links {
			s += math.Hypot(ps[lk[1]].X-ps[lk[0]].X, ps[lk[1]].Y-ps[lk[0]].Y)
		}
		return s
	}
	before := Violations(out, minDX, minDY)
	work := make([]Point, len(out))
	copy(work, out)
	linkSwapPass(work, minDX, minDY, links, groupOf)
	if Violations(work, minDX, minDY) > before {
		copy(work, out)
	}
	// Rescue any link the strict pass could not move (its
	// attempts are self-guarding; the check is a second belt).
	linkSwapRescue(work, minDX, minDY, aspect, links, groupOf)
	if Violations(work, minDX, minDY) > before {
		copy(work, out)
	}
	swapCost := totalCost(work)
	anchor := make([]Point, len(work))
	copy(anchor, work)
	work2 := make([]Point, len(work))
	copy(work2, work)
	const rounds = 80
	for r := 0; r < rounds; r++ {
		linkStep(work2, links, minDX, minDY, 0.15)
		for i := range work2 {
			work2[i].X += (anchor[i].X - work2[i].X) * 0.003
			work2[i].Y += (anchor[i].Y - work2[i].Y) * 0.003
		}
		for p := 0; p < 2; p++ {
			push := pusher(work2, minDX, minDY, axisBias(work2, aspect))
			for i := 0; i < len(work2); i++ {
				for j := i + 1; j < len(work2); j++ {
					if !separatedPair(work2[i], work2[j], minDX, minDY) {
						push(i, j)
					}
				}
			}
		}
	}
	// Contraction piles nodes up, so the plain settle can end
	// jammed; the finish phase (weak re-relaxation alternating
	// with settles) is the part of the pipeline built to un-jam
	// a dense packing, so the polish ends with it before the
	// violation guard judges the result.
	finish(work2, anchor, minDX, minDY, aspect, links)
	// The springs result wins only when it is at least as good
	// on BOTH axes as the swap stage: no extra violations and
	// no greater total link cost. (Violations alone are not
	// enough — a spring drift can satisfy every footprint while
	// quietly stretching the very links the swaps shortened.)
	if Violations(work2, minDX, minDY) <= before && totalCost(work2) <= swapCost {
		copy(out, work2)
		return
	}
	copy(out, work)
}

// settlePhase runs pure projection passes, no spring, so the
// returned arrangement honours the minimum distances as exactly
// as the packing allows. The sweep direction alternates per pass:
// a fixed visit order can leave a dense packing cycling the same
// few pushes back and forth, and reversing the order breaks the
// cycle.
func settlePhase(out []Point, minDX, minDY, aspect float64) {
	const settlePasses = 400
	for pass := 0; pass < settlePasses; pass++ {
		push := pusher(out, minDX, minDY, axisBias(out, aspect))
		violations := 0
		if pass%2 == 0 {
			for i := 0; i < len(out); i++ {
				for j := i + 1; j < len(out); j++ {
					if !separatedPair(out[i], out[j], minDX, minDY) {
						violations++
						push(i, j)
					}
				}
			}
		} else {
			for i := len(out) - 1; i >= 0; i-- {
				for j := len(out) - 1; j > i; j-- {
					if !separatedPair(out[i], out[j], minDX, minDY) {
						violations++
						push(i, j)
					}
				}
			}
		}
		if violations == 0 {
			break
		}
	}
}

// finish settles the arrangement, and if a dense packing is still
// jammed (pairs left closer than their minima), it alternates
// shorter relaxation rounds — whose anchor spring un-jams the
// pack — with fresh settles, a fixed number of times. The result
// is deterministic: fixed round and pass counts throughout.
func finish(out, orig []Point, minDX, minDY, aspect float64, links [][]int) {
	settlePhase(out, minDX, minDY, aspect)
	for r := 0; r < 3 && Violations(out, minDX, minDY) > 0; r++ {
		relaxSpring(out, orig, minDX, minDY, aspect, 80, 0.003, links)
		settlePhase(out, minDX, minDY, aspect)
	}
}

// Options carries the optional steering inputs for a tidy run.
type Options struct {
	// Aspect is the target width / height ratio of the arranged
	// span (see axisBias). Zero disables the steering.
	Aspect float64
	// BoxW and BoxH give the size of the canvas the arrangement
	// will be shown on, in point units. When both are set and
	// the relaxed arrangement overflows the box, the layout also
	// tries a shelf-packed arrangement (see shelfSeed) and keeps
	// whichever fits the box better — dense drawings whose
	// labels need more room than the stored span offers cannot
	// be repacked by pairwise nudges alone.
	BoxW, BoxH float64
	// Links are the drawing's link pairs as point indices
	// (v3.4.4): linked points are pulled toward adjacency during
	// the layout (see the package comment). Nil or empty keeps
	// the pre-v3.4.4 behaviour exactly.
	Links [][]int
}

// Tidy returns a new position for every input point, in the same
// order. minDX and minDY are the minimum centre distances (in the
// points' own units) along each axis. Points whose IDs repeat are
// treated as independent nodes.
func Tidy(pts []Point, minDX, minDY float64) []Point {
	return TidyAspect(pts, minDX, minDY, 0)
}

// TidyOpts is Tidy with steering options.
func TidyOpts(pts []Point, minDX, minDY float64, opts Options) []Point {
	return tidyRun(pts, minDX, minDY, opts)
}

func tidyRun(pts []Point, minDX, minDY float64, opts Options) []Point {
	out, _ := tidyRunDiag(pts, minDX, minDY, opts)
	return out
}

// TidyDiag is TidyOpts with diagnostics: whether the packed
// seed produced the result.
func TidyDiag(pts []Point, minDX, minDY float64, opts Options) ([]Point, bool) {
	return tidyRunDiag(pts, minDX, minDY, opts)
}

// tidyRunDiag is tidyRun with diagnostics: whether the packed
// seed produced the result.
func tidyRunDiag(pts []Point, minDX, minDY float64, opts Options) ([]Point, bool) {
	out := make([]Point, len(pts))
	copy(out, pts)
	if len(out) < 2 || minDX <= 0 || minDY <= 0 {
		return out, false
	}
	orig := make([]Point, len(pts))
	copy(orig, pts)
	relaxPhase(out, orig, minDX, minDY, opts.Aspect, opts.Links)
	finish(out, orig, minDX, minDY, opts.Aspect, opts.Links)
	out, seeded := maybeSeed(pts, out, minDX, minDY, nil, opts)
	linkPolish(out, minDX, minDY, opts.Aspect, opts.Links, nil)
	return out, seeded
}

// maybeSeed implements the shelf-packing fallback shared by the
// tidy entry points: when a canvas box is known and the relaxed
// arrangement either overflows it or still leaves pairs closer
// than their minima (a locally jammed cluster that pairwise
// nudges cannot repack), a shelf-packed arrangement is built
// and settled, and it replaces the relaxed one when it honours
// the footprint contract better — fewer violations wins (the
// footprints are the space each label needs — an arrangement
// that leaves pairs inside them hands the label planner
// problems it may not always solve), and the box fit only
// breaks ties. Sparse drawings, where the relaxed arrangement
// is already violation-free, keep their organic stored look.
// Without a box the relaxed arrangement is returned unchanged.
// The second result reports whether the seed was used.
func maybeSeed(pts, out []Point, minDX, minDY float64, groups [][]int, opts Options) ([]Point, bool) {
	if opts.BoxW <= 0 || opts.BoxH <= 0 {
		return out, false
	}
	vOut := Violations(out, minDX, minDY)
	if vOut == 0 && boxScore(out, opts) <= 1.02 {
		return out, false
	}
	seeded := shelfSeed(pts, minDX, minDY, groups, opts)
	sorig := make([]Point, len(seeded))
	copy(sorig, seeded)
	finish(seeded, sorig, minDX, minDY, opts.Aspect, opts.Links)
	vSeed := Violations(seeded, minDX, minDY)
	sOut, sSeed := boxScore(out, opts), boxScore(seeded, opts)
	if vSeed < vOut || (vSeed == vOut && sSeed < sOut) {
		return seeded, true
	}
	return out, false
}

// TidyAspect is Tidy with a target aspect (width / height) for
// the arranged drawing: the separation pushes prefer the axis
// that moves the drawing's span toward the target (see
// axisBias), so a packing destined for a wide, short canvas
// spreads sideways instead of piling into a tall column. A zero
// aspect disables the preference.
func TidyAspect(pts []Point, minDX, minDY, aspect float64) []Point {
	return tidyRun(pts, minDX, minDY, Options{Aspect: aspect})
}

// TidySnap is Tidy with a grid-snap pass in the middle: after the
// relaxation, coordinates within tolX / tolY of each other inside
// a group are snapped onto shared lines (see SnapGroups), and the
// settle projection runs again so the snap cannot leave any pair
// closer than the minima.
func TidySnap(pts []Point, minDX, minDY float64, groups [][]int, tolX, tolY float64) []Point {
	return TidySnapAspect(pts, minDX, minDY, groups, tolX, tolY, 0)
}

// TidySnapAspect is TidySnap with a target aspect for the
// arranged drawing (see TidyAspect).
func TidySnapAspect(pts []Point, minDX, minDY float64, groups [][]int, tolX, tolY, aspect float64) []Point {
	return TidySnapOpts(pts, minDX, minDY, groups, tolX, tolY, Options{Aspect: aspect})
}

// TidySnapOpts is TidySnap with steering options.
func TidySnapOpts(pts []Point, minDX, minDY float64, groups [][]int, tolX, tolY float64, opts Options) []Point {
	out, _ := TidySnapDiag(pts, minDX, minDY, groups, tolX, tolY, opts)
	return out
}

// TidySnapDiag is TidySnapOpts with diagnostics: whether the
// packed seed produced the result.
func TidySnapDiag(pts []Point, minDX, minDY float64, groups [][]int, tolX, tolY float64, opts Options) ([]Point, bool) {
	out := make([]Point, len(pts))
	copy(out, pts)
	seeded := false
	if len(out) < 2 || minDX <= 0 || minDY <= 0 {
		return out, seeded
	}
	orig := make([]Point, len(pts))
	copy(orig, pts)
	relaxPhase(out, orig, minDX, minDY, opts.Aspect, opts.Links)
	out = SnapGroups(out, groups, tolX, tolY)
	// The snap moved points (by at most the tolerance) after the
	// relaxation, so the finish phase needs the relaxed positions
	// as its anchor reference, not the pre-snap ones.
	orig = make([]Point, len(out))
	copy(orig, out)
	finish(out, orig, minDX, minDY, opts.Aspect, opts.Links)
	// The seed is not snapped: its rows and slots are exact by
	// construction, and snapping would only jam it.
	out, seeded = maybeSeed(pts, out, minDX, minDY, groups, opts)
	// Link polish last (v3.4.4): whichever arrangement won, its
	// linked pairs are settled adjacent. The polish runs after
	// the snap on purpose — snapping first keeps rows straight,
	// and the polish's own separation projections keep the
	// footprint contract while partners come together. Swaps
	// stay inside a group (one map region), so no node is ever
	// permuted into another region's box.
	groupOf := make([]int, len(out))
	for gi, g := range groupSets(len(out), groups) {
		for _, i := range g {
			groupOf[i] = gi
		}
	}
	linkPolish(out, minDX, minDY, opts.Aspect, opts.Links, groupOf)
	return out, seeded
}

// spanOf returns the width and height of the points' bounding
// box (centre span; footprints are not added).
func spanOf(pts []Point) (float64, float64) {
	if len(pts) == 0 {
		return 0, 0
	}
	minX, maxX, minY, maxY := pts[0].X, pts[0].X, pts[0].Y, pts[0].Y
	for _, p := range pts[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	return maxX - minX, maxY - minY
}

// boxScore rates an arrangement against the target box: the
// larger of the two span/box ratios. 1.0 means the span exactly
// fills the box on one axis; above 1 the drawing must shrink to
// fit.
func boxScore(pts []Point, opts Options) float64 {
	w, h := spanOf(pts)
	return math.Max(w/opts.BoxW, h/opts.BoxH)
}

// SnapGroups returns a copy of pts in which, inside each group,
// X coordinates (and separately Y coordinates) that lie within
// the tolerance of each other are moved onto one shared value —
// the cluster's lower median — so near-rows and near-columns come
// out exactly straight. Groups are index lists into pts; a point
// belongs to the first group that names it, and points named by
// no group form one leftover group of their own (so a drawing
// with no groups at all is snapped globally). Snapping never
// moves a point by more than the tolerance. Deterministic: the
// clustering walks values in sorted order with index tie-breaks,
// and each cluster is anchored at its smallest member so clusters
// cannot chain-drift.
func SnapGroups(pts []Point, groups [][]int, tolX, tolY float64) []Point {
	out := make([]Point, len(pts))
	copy(out, pts)
	if len(out) == 0 {
		return out
	}
	for _, g := range groupSets(len(out), groups) {
		snapAxis(out, g, tolX, true)
		snapAxis(out, g, tolY, false)
	}
	return out
}

// groupSets cleans raw group index lists against n points: out-
// of-range indices are dropped, a point belongs to the first
// group that names it, and points named by no group form one
// leftover group of their own.
func groupSets(n int, groups [][]int) [][]int {
	seen := make([]bool, n)
	var gs [][]int
	for _, g := range groups {
		var clean []int
		for _, i := range g {
			if i >= 0 && i < n && !seen[i] {
				seen[i] = true
				clean = append(clean, i)
			}
		}
		if len(clean) > 0 {
			gs = append(gs, clean)
		}
	}
	var left []int
	for i := 0; i < n; i++ {
		if !seen[i] {
			left = append(left, i)
		}
	}
	if len(left) > 0 {
		gs = append(gs, left)
	}
	return gs
}

// linkAdjacency builds the neighbour lists for a link set:
// index i's neighbours, sorted ascending, duplicates removed.
// Out-of-range and self links are ignored.
func linkAdjacency(n int, links [][]int) [][]int {
	adj := make([][]int, n)
	for _, lk := range links {
		if len(lk) != 2 {
			continue
		}
		i, j := lk[0], lk[1]
		if i < 0 || j < 0 || i >= n || j >= n || i == j {
			continue
		}
		adj[i] = append(adj[i], j)
		adj[j] = append(adj[j], i)
	}
	for i := range adj {
		sort.Ints(adj[i])
		uniq := adj[i][:0]
		for k, v := range adj[i] {
			if k == 0 || v != adj[i][k-1] {
				uniq = append(uniq, v)
			}
		}
		adj[i] = uniq
	}
	return adj
}

// bfsOrder reorders one group's reading-ordered member list by
// a breadth-first walk over the link graph: starting from the
// first unvisited member in reading order, each member is
// followed by its in-group link partners (in reading order), so
// partners receive neighbouring slots when the group is packed.
// Members with no in-group links keep their reading order.
func bfsOrder(reading []int, adj [][]int) []int {
	inGroup := make(map[int]bool, len(reading))
	rank := make(map[int]int, len(reading))
	for pos, idx := range reading {
		inGroup[idx] = true
		rank[idx] = pos
	}
	seen := make(map[int]bool, len(reading))
	out := make([]int, 0, len(reading))
	for _, start := range reading {
		if seen[start] {
			continue
		}
		queue := []int{start}
		seen[start] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			out = append(out, cur)
			var nbrs []int
			for _, nb := range adj[cur] {
				if inGroup[nb] && !seen[nb] {
					nbrs = append(nbrs, nb)
				}
			}
			sort.Slice(nbrs, func(a, b int) bool { return rank[nbrs[a]] < rank[nbrs[b]] })
			for _, nb := range nbrs {
				seen[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	return out
}

// shelfSeed builds an arrangement from scratch by shelf packing:
// one global pack over canvas-wide rows, groups in stored
// centroid reading order and never sharing a row, members in
// stored reading order (sorted by Y, then X, then index), each
// row exactly as tall as its tallest footprint and each slot
// exactly as wide as its footprint (plus a small allowance), so
// the seed satisfies every pairwise footprint separation inside
// a group by construction. The settle phase that follows only
// has the row seams between groups to polish. The whole seed is
// translated onto the stored drawing's centroid, so the map
// stays where it was. Deterministic: every sort has an index
// tie-break.
func shelfSeed(pts []Point, minDX, minDY float64, groups [][]int, opts Options) []Point {
	// Slots are sized a little above the bare footprint: an
	// exactly-tight pack leaves the settle projection no room
	// to absorb the row seams.
	wOf := func(p Point) float64 {
		if p.FW > minDX {
			return p.FW + minDX*0.03
		}
		return minDX * 1.03
	}
	hOf := func(p Point) float64 {
		if p.FH > minDY {
			return p.FH + minDY*0.03
		}
		return minDY * 1.03
	}
	// One global shelf pack over canvas-wide rows. Groups keep
	// their reading order (by stored centroid) and never share
	// a row — a group always starts on a fresh row — so each
	// group's members stay together in whole rows, while the
	// rows themselves are filled to the full canvas width.
	// Slots are top-aligned: a member's slot is its own
	// footprint, the row is as tall as its tallest slot, and
	// with slots side by side at exactly their widths, every
	// pair inside a group is footprint-separated by
	// construction.
	gs := groupSets(len(pts), groups)
	centroid := make([][2]float64, len(gs))
	for gi, g := range gs {
		var cx, cy float64
		for _, i := range g {
			cx += pts[i].X
			cy += pts[i].Y
		}
		if len(g) > 0 {
			centroid[gi] = [2]float64{cx / float64(len(g)), cy / float64(len(g))}
		}
	}
	order := make([]int, len(gs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ca, cb := centroid[order[a]], centroid[order[b]]
		if ca[1] != cb[1] {
			return ca[1] < cb[1]
		}
		if ca[0] != cb[0] {
			return ca[0] < cb[0]
		}
		return order[a] < order[b]
	})
	// Link-aware packing order (v3.4.4): when links are known,
	// groups are packed greedily — after the first group (reading
	// order), each next group is the one with the most links to
	// the groups already placed — so region boxes that are joined
	// by links land in neighbouring rows of the pack instead of
	// at opposite ends of it. Ties keep the reading order.
	var adj [][]int
	if len(opts.Links) > 0 {
		adj = linkAdjacency(len(pts), opts.Links)
		groupOf := make([]int, len(pts))
		for gi, g := range gs {
			for _, i := range g {
				groupOf[i] = gi
			}
		}
		weight := make(map[[2]int]int)
		for _, lk := range opts.Links {
			if len(lk) != 2 || lk[0] < 0 || lk[1] < 0 || lk[0] >= len(pts) || lk[1] >= len(pts) {
				continue
			}
			ga, gb := groupOf[lk[0]], groupOf[lk[1]]
			if ga == gb {
				continue
			}
			if ga > gb {
				ga, gb = gb, ga
			}
			weight[[2]int{ga, gb}]++
		}
		if len(weight) > 0 {
			rank := make([]int, len(gs))
			for pos, gi := range order {
				rank[gi] = pos
			}
			placed := make([]bool, len(gs))
			greedy := make([]int, 0, len(gs))
			cur := order[0]
			for len(greedy) < len(gs) {
				greedy = append(greedy, cur)
				placed[cur] = true
				best, bestW := -1, -1
				for _, cand := range order {
					if placed[cand] {
						continue
					}
					w := 0
					for _, p := range greedy {
						a, b := p, cand
						if a > b {
							a, b = b, a
						}
						w += weight[[2]int{a, b}]
					}
					if w > bestW || (w == bestW && (best < 0 || rank[cand] < rank[best])) {
						best, bestW = cand, w
					}
				}
				if best < 0 {
					break
				}
				cur = best
			}
			order = greedy
		}
	}
	out := make([]Point, len(pts))
	copy(out, pts)
	var x, y, rowH float64
	newRow := func() {
		y += rowH
		x, rowH = 0, 0
	}
	for _, gi := range order {
		g := gs[gi]
		sorted := make([]int, len(g))
		copy(sorted, g)
		sort.Slice(sorted, func(a, b int) bool {
			pa, pb := pts[sorted[a]], pts[sorted[b]]
			if pa.Y != pb.Y {
				return pa.Y < pb.Y
			}
			if pa.X != pb.X {
				return pa.X < pb.X
			}
			return sorted[a] < sorted[b]
		})
		// Link-aware member order (v3.4.4): walk the group's link
		// graph breadth-first (in reading order) so linked
		// members receive neighbouring slots in the pack.
		if adj != nil {
			sorted = bfsOrder(sorted, adj)
		}
		// A group starts on a fresh row — unless it fits
		// ENTIRELY into the current row's remaining width, in
		// which case sharing the row saves a whole row of
		// height (the two groups stay side by side, never
		// interleaved).
		if x > 0 {
			total := 0.0
			for _, i := range sorted {
				total += wOf(pts[i])
			}
			if total > opts.BoxW-x {
				newRow()
			}
		}
		for _, i := range sorted {
			w, h := wOf(pts[i]), hOf(pts[i])
			if x > 0 && x+w > opts.BoxW {
				newRow()
			}
			out[i].X = x + w/2
			out[i].Y = y + h/2
			x += w
			if h > rowH {
				rowH = h
			}
		}
	}
	// Translate so the seed's centroid matches the stored one:
	// the arrangement replaces the drawing in place instead of
	// teleporting it to the origin.
	var ocx, ocy, ncx, ncy float64
	for i := range pts {
		ocx += pts[i].X
		ocy += pts[i].Y
		ncx += out[i].X
		ncy += out[i].Y
	}
	if len(pts) > 0 {
		dx := (ocx - ncx) / float64(len(pts))
		dy := (ocy - ncy) / float64(len(pts))
		for i := range out {
			out[i].X += dx
			out[i].Y += dy
		}
	}
	return out
}

// snapAxis snaps one coordinate of the points in idxs: values are
// clustered in ascending order; a cluster holds every value within
// tol of the cluster's smallest value, and all of them move to the
// cluster's lower median (an actual member value).
func snapAxis(out []Point, idxs []int, tol float64, xAxis bool) {
	if tol <= 0 || len(idxs) < 2 {
		return
	}
	type ve struct {
		v float64
		i int
	}
	ves := make([]ve, len(idxs))
	for k, i := range idxs {
		v := out[i].Y
		if xAxis {
			v = out[i].X
		}
		ves[k] = ve{v, i}
	}
	sort.Slice(ves, func(a, b int) bool {
		if ves[a].v != ves[b].v {
			return ves[a].v < ves[b].v
		}
		return ves[a].i < ves[b].i
	})
	for start := 0; start < len(ves); {
		end := start + 1
		for end < len(ves) && ves[end].v-ves[start].v <= tol {
			end++
		}
		if end-start > 1 {
			med := ves[(start+end-1)/2].v
			for k := start; k < end; k++ {
				if xAxis {
					out[ves[k].i].X = med
				} else {
					out[ves[k].i].Y = med
				}
			}
		}
		start = end
	}
}

// Violations counts the pairs that are closer than their minima
// (the call-level minima raised by the pairs' footprints) on both
// axes. It is exported for tests and diagnostics; the tidy
// pipeline itself does not need it.
func Violations(pts []Point, minDX, minDY float64) int {
	n := 0
	for i := 0; i < len(pts); i++ {
		for j := i + 1; j < len(pts); j++ {
			if !separatedPair(pts[i], pts[j], minDX, minDY) {
				n++
			}
		}
	}
	return n
}
