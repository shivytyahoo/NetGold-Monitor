package layout

// Grid-first tidy layout.
//
// The heuristic tidy pipeline (relax, snap, springs, swaps) kept
// trading one visual defect for another: every pass fixed the
// metric it targeted and broke a different one. The grid layout
// replaces it for callers that ask for it: correctness holds by
// construction instead of by repair.
//
// The model is the renderer's own geometry. In grid mode a
// point's footprint means: FW is the width of the label pill the
// renderer will draw under the marker (centred on it), and FH is
// how far that label stack reaches below the marker centre. The
// marker itself needs gridMarkerTop px above its centre (the
// renderer's marker collision half-extent). A device therefore
// reserves a rectangle FW wide and gridMarkerTop+FH tall, with
// the label always below the marker inside it.
//
// Devices are laid out per region box on a clean grid of such
// cells: one row pitch per box and one column width per column
// (the widest reserved rectangle in that column, plus a clear
// gap), members ordered by a breadth-first walk over the
// drawing's link graph (so link partners land in neighbouring
// cells) and filled serpentine. A member whose links cross into
// another box is assigned first, to the cell on the side of its
// box that faces the partner box (by stored direction), so a
// cross-box link leaves from the facing edge instead of running
// diagonally across the grid. Region boxes form a forest by
// stored containment; a box's drawn rectangle is computed
// bottom-up from its own grid plus its child boxes, blocks
// inside a box are placed at their stored centres and pushed
// apart deterministically when they collide, and the same
// placement runs once more over the top-level boxes on the
// canvas. Points that belong to no box form the loose group,
// which is packed like a box and reports its own drawn
// rectangle (see GridResult.Loose) so the renderer can frame
// it instead of leaving its devices floating at the canvas
// edge. Nothing is iterative at the device level: given the
// same input the output is identical, every reserved rectangle
// sits inside its box's drawn rectangle, sibling rectangles
// never overlap, and no two reserved rectangles overlap.

import (
	"math"
	"sort"
)

const (
	// gridMarkerTop is the reserved extent above a marker centre:
	// the marker plate plus its badge spill, matching the web
	// renderer's marker collision half-extent.
	gridMarkerTop = 25.0
	// gridGapX is the clear space between neighbouring pill edges
	// in a row; gridGapY is the clear space between a label's
	// bottom edge and the next row's marker top.
	gridGapX = 14.0
	gridGapY = 14.0
	// gridBoxPad is the clear space between a box's content and
	// its drawn border; gridBoxGap is the clear space kept
	// between sibling blocks while packing them. Both are wide
	// enough to stay clearly visible at the fit ratios dense
	// drawings render at: a border-hugging label or a hairline
	// between two boxes reads as a defect even when the reserved
	// rectangles are technically clear.
	gridBoxPad = 16.0
	gridBoxGap = 28.0
	// The band a grid block's own aspect (width / height) is
	// kept inside by adjusting its column count. A stored shape
	// can be an extreme strip — a column of devices drawn for a
	// tall console window — and a grid that kept that shape
	// could never fit a landscape screen whole; a block flatter
	// than the band's top wastes the screen the other way.
	gridBlockAspectMin = 0.55
	gridBlockAspectMax = 3.0
)

// GridBox is one region box for the grid layout: the points that
// belong to it directly (its own members — devices whose smallest
// containing square it is), its stored envelope in the caller's
// units, and its parent box (by index into the boxes slice) or
// -1 for a top-level box.
type GridBox struct {
	ID      int64
	Members []int
	Env     [4]float64 // x1, y1, x2, y2 (y grows up)
	Parent  int
}

// GridResult is the outcome of TidyGrid: the arranged points
// (same order as the input), the drawn rectangle of every input
// box (same order), and each point's grid column pitch (the cell
// width its label budget should use; zero for no point — every
// point is placed in a cell, so it is always set). When points
// belong to no box, Loose holds the loose group's own drawn
// rectangle and HasLoose is true, so the renderer can frame
// those devices like the boxed ones.
type GridResult struct {
	Points   []Point
	Boxes    [][4]float64
	CellW    []float64
	Loose    [4]float64
	HasLoose bool
}

// gridCtx carries the cross-box link context the facing-edge
// cell assignment needs: the box that owns each point (-1 for
// the loose group), every box's centre (stored centres in the
// first pass — the loose group's is its members' stored
// centroid — and the first pass's drawn centres in the
// refinement pass, see gridRefine), and the link adjacency
// over all points.
type gridCtx struct {
	owner  []int
	centre map[int][2]float64
	adj    [][]int
}

// gridRefine is the refinement pass's input: the first pass's
// drawn box centres, which replace the stored centres as the
// facing assignment's aim points. Drawn centres are stable
// targets — a box's centre moves far less between passes than
// any single device's cell does — so the refinement cannot
// chase its own tail.
type gridRefine struct {
	centre map[int][2]float64
}

// gNode is one layout node: a region box, or the group of points
// that belong to no box (the loose group, box == -1). Children
// are the boxes stored directly inside it.
type gNode struct {
	box      int
	members  []int // point indices, in fill order
	children []*gNode

	cols, rows       int
	pitchX, pitchY   float64
	maxW, maxH       float64 // widest / tallest reserved rectangle
	colW, colX       []float64
	assign           []int   // fill index -> index into members
	ownW, ownH       float64 // own grid block size
	w, h             float64 // total block size (drawn size for boxes)
	scx, scy         float64 // stored anchor centre
	ownOffX, ownOffY float64 // own block centre, relative to this node's rect centre
	childOffX        []float64
	childOffY        []float64
	rect             [4]float64 // placed drawn rectangle (absolute)
}

// TidyGrid arranges pts on per-box grids as described at the top
// of this file. links are point-index pairs used only to order
// the fill. opts.Aspect is the canvas width/height ratio, used
// only as the fallback aspect for a group whose stored shape is
// degenerate. The result is fully deterministic.
func TidyGrid(pts []Point, minDX, minDY float64, boxes []GridBox, links [][]int, opts Options) GridResult {
	n := len(pts)
	out := make([]Point, n)
	copy(out, pts)
	res := GridResult{Points: out, Boxes: make([][4]float64, len(boxes)), CellW: make([]float64, n)}
	if n == 0 {
		for i, b := range boxes {
			res.Boxes[i] = normEnv(b.Env)
		}
		return res
	}

	// Global fill order: a breadth-first walk over the link graph
	// (reading order breaks every tie), so link partners receive
	// neighbouring ranks and, inside any one box, neighbouring
	// cells. Points in no link keep their reading order.
	adj := linkAdjacency(n, links)
	reading := make([]int, n)
	for i := range reading {
		reading[i] = i
	}
	rank := make([]int, n)
	for pos, idx := range bfsOrder(reading, adj) {
		rank[idx] = pos
	}

	// runLayout builds the box forest, sizes it bottom-up and
	// places it, honouring per-node column overrides (keyed by
	// box index; the loose group is key -1). It also reports the
	// column count every node ended up with and each node's
	// member count, for the optimisation pass below.
	runLayout := func(overrides map[int]int, ref *gridRefine) (GridResult, map[int]int, map[int]int) {
		out := make([]Point, n)
		copy(out, pts)
		res := GridResult{Points: out, Boxes: make([][4]float64, len(boxes)), CellW: make([]float64, n)}
		colsUsed := map[int]int{}
		counts := map[int]int{}

		// Sanitise the box forest: a point belongs to the first
		// box that claims it; a parent pointer must name another
		// box and must not form a cycle.
		claimed := make([]bool, n)
		nodes := make([]*gNode, len(boxes))
		for i, b := range boxes {
			nd := &gNode{box: i}
			for _, m := range b.Members {
				if m >= 0 && m < n && !claimed[m] {
					claimed[m] = true
					nd.members = append(nd.members, m)
				}
			}
			env := normEnv(b.Env)
			nd.scx = (env[0] + env[2]) / 2
			nd.scy = (env[1] + env[3]) / 2
			nodes[i] = nd
		}
		parent := make([]int, len(boxes))
		for i := range parent {
			parent[i] = -1
		}
		for i, b := range boxes {
			p := b.Parent
			if p < 0 || p >= len(boxes) || p == i {
				p = -1
			}
			parent[i] = p
		}
		// Cycle guard, run only once every parent is assigned
		// (the zero value of an unassigned entry must never be
		// mistaken for a pointer to box 0): a box whose ancestor
		// chain loops — back to itself, or into a cycle it
		// points into — becomes a root, which breaks the cycle
		// deterministically.
		for i := range boxes {
			seen := make(map[int]bool)
			for q := parent[i]; q >= 0; q = parent[q] {
				if q == i || seen[q] {
					parent[i] = -1
					break
				}
				seen[q] = true
			}
		}
		var roots []*gNode
		for i, nd := range nodes {
			if parent[i] >= 0 {
				nodes[parent[i]].children = append(nodes[parent[i]].children, nd)
			} else {
				roots = append(roots, nd)
			}
		}
		// Points claimed by no box form the loose group, a
		// root-level node whose rectangle the result reports
		// separately (GridResult.Loose) so it can be framed
		// like a box.
		var looseMembers []int
		for i := 0; i < n; i++ {
			if !claimed[i] {
				looseMembers = append(looseMembers, i)
			}
		}
		if len(looseMembers) > 0 {
			lnd := &gNode{box: -1, members: looseMembers}
			var cx, cy float64
			for _, m := range looseMembers {
				cx += pts[m].X
				cy += pts[m].Y
			}
			lnd.scx = cx / float64(len(looseMembers))
			lnd.scy = cy / float64(len(looseMembers))
			roots = append(roots, lnd)
		}
		// Order members by global rank once, before sizing.
		for _, nd := range nodes {
			sort.Slice(nd.members, func(a, b int) bool { return rank[nd.members[a]] < rank[nd.members[b]] })
		}
		if len(looseMembers) > 0 {
			lnd := roots[len(roots)-1]
			sort.Slice(lnd.members, func(a, b int) bool { return rank[lnd.members[a]] < rank[lnd.members[b]] })
		}

		// Cross-box link context for the facing-edge assignment
		// (see assignCells): which box owns each point, and every
		// box's stored centre.
		ctx := &gridCtx{owner: make([]int, n), centre: map[int][2]float64{}, adj: adj}
		for i := range ctx.owner {
			ctx.owner[i] = -1
		}
		for _, nd := range nodes {
			for _, m := range nd.members {
				ctx.owner[m] = nd.box
			}
			env := normEnv(boxes[nd.box].Env)
			ctx.centre[nd.box] = [2]float64{(env[0] + env[2]) / 2, (env[1] + env[3]) / 2}
		}
		if len(looseMembers) > 0 {
			var cx, cy float64
			for _, m := range looseMembers {
				cx += pts[m].X
				cy += pts[m].Y
			}
			ctx.centre[-1] = [2]float64{cx / float64(len(looseMembers)), cy / float64(len(looseMembers))}
		}
		if ref != nil {
			// Refinement pass: aim with the first pass's drawn
			// centres instead of the stored centres.
			for k, c := range ref.centre {
				ctx.centre[k] = c
			}
		}

		// Size every subtree bottom-up, then place the roots on
		// the canvas at their stored centres (pushing apart on
		// collision) and assign absolute positions top-down.
		for _, r := range roots {
			sizeNode(r, pts, boxes, minDX, minDY, opts, overrides, ctx)
		}
		blocks := make([]gBlock, len(roots))
		for i, r := range roots {
			blocks[i] = gBlock{w: r.w, h: r.h, cx: r.scx, cy: r.scy}
		}
		placeBlocks(blocks)
		for i, r := range roots {
			r.rect = [4]float64{blocks[i].cx - r.w/2, blocks[i].cy - r.h/2, blocks[i].cx + r.w/2, blocks[i].cy + r.h/2}
			placeNode(r, out, res.CellW)
			if r.box == -1 {
				res.Loose, res.HasLoose = r.rect, true
			}
		}
		for _, nd := range nodes {
			res.Boxes[nd.box] = nd.rect
		}
		// Any box not reached from the roots (impossible after
		// the cycle guard, but keep the result total) reports
		// its stored envelope.
		for i, b := range boxes {
			if res.Boxes[i] == [4]float64{} {
				res.Boxes[i] = normEnv(b.Env)
			}
		}
		var collect func(nd *gNode)
		collect = func(nd *gNode) {
			if len(nd.members) > 0 {
				colsUsed[nd.box] = nd.cols
				counts[nd.box] = len(nd.members)
			}
			for _, ch := range nd.children {
				collect(ch)
			}
		}
		for _, r := range roots {
			collect(r)
		}
		return res, colsUsed, counts
	}

	res, colsUsed, counts := runLayout(nil, nil)
	var finalOverrides map[int]int
	// Column optimisation: when the caller names the canvas
	// (opts.BoxW/BoxH), a box's best column count depends on
	// its siblings — three tall boxes in a row fit the screen
	// better slightly wider each, a deep box better narrower.
	// A bounded greedy pass tries one more and one fewer
	// column per node (largest first), keeping a change only
	// when the whole scene's fit ratio against the canvas
	// improves. Deterministic: fixed order, strict improvement
	// threshold, fixed trial budget. Without a canvas the
	// aspect-derived counts stand.
	if opts.BoxW > 0 && opts.BoxH > 0 && len(colsUsed) > 0 {
		best := gridSceneFit(res, opts.BoxW, opts.BoxH)
		overrides := map[int]int{}
		keys := make([]int, 0, len(colsUsed))
		for key := range colsUsed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(a, b int) bool {
			if counts[keys[a]] != counts[keys[b]] {
				return counts[keys[a]] > counts[keys[b]]
			}
			return keys[a] < keys[b]
		})
		base := func(key int) int {
			if v, ok := overrides[key]; ok {
				return v
			}
			return colsUsed[key]
		}
		trials := 0
		done := false
		for round := 0; round < 6 && !done; round++ {
			improved := false
			for _, key := range keys {
				for _, d := range []int{1, -1} {
					nc := base(key) + d
					if nc < 1 || nc > counts[key] {
						continue
					}
					trial := make(map[int]int, len(overrides)+1)
					for k2, v := range overrides {
						trial[k2] = v
					}
					trial[key] = nc
					r2, cols2, _ := runLayout(trial, nil)
					trials++
					if f2 := gridSceneFit(r2, opts.BoxW, opts.BoxH); f2 > best+0.0015 {
						res, best, overrides, colsUsed = r2, f2, trial, cols2
						improved = true
						break
					}
					if trials >= 48 {
						done = true
						break
					}
				}
				if done {
					break
				}
			}
			if !improved {
				break
			}
		}
		finalOverrides = overrides
	}
	// Facing refinement: the first pass aimed cross-box members
	// at the partner box's STORED centre, but packing can move a
	// box well away from its stored position, so the aim can
	// point the wrong way in the drawn scene. When any link
	// crosses a box boundary, run the layout once more aiming at
	// the first pass's DRAWN centres instead (each member's own
	// box centre likewise). Column counts (including the
	// optimisation's) are kept; only the assignment, and with it
	// the column widths, are redone. Deterministic: the first
	// pass is.
	if len(adj) > 0 {
		ownerOf := make([]int, n)
		for i := range ownerOf {
			ownerOf[i] = -1
		}
		claimed := make([]bool, n)
		for bi, b := range boxes {
			for _, m := range b.Members {
				if m >= 0 && m < n && !claimed[m] {
					claimed[m] = true
					ownerOf[m] = bi
				}
			}
		}
		cross := false
		for i := 0; i < n && !cross; i++ {
			for _, q := range adj[i] {
				if ownerOf[i] != ownerOf[q] {
					cross = true
					break
				}
			}
		}
		if cross {
			centres := make(map[int][2]float64, len(res.Boxes)+1)
			for i, r := range res.Boxes {
				centres[i] = [2]float64{(r[0] + r[2]) / 2, (r[1] + r[3]) / 2}
			}
			if res.HasLoose {
				centres[-1] = [2]float64{(res.Loose[0] + res.Loose[2]) / 2, (res.Loose[1] + res.Loose[3]) / 2}
			}
			refined, _, _ := runLayout(finalOverrides, &gridRefine{centre: centres})
			res = refined
		}
	}
	// Uncrossing pass: two links that join the same pair of
	// boxes can still cross in the space between the boxes when
	// the endpoint order along the facing sides disagrees — the
	// northern endpoint on one side paired with the southern
	// endpoint on the other, and vice versa. The grid is fixed
	// by then, so the pass swaps the positions (and cell widths)
	// of two devices of one box whenever a swap removes
	// crossings without breaking an invariant: both devices'
	// reserved rectangles stay inside their owner's drawn
	// rectangle and clear of every other device. General and
	// data-driven — any map, any boxes, no names.
	if len(links) > 0 {
		ownerOf := make([]int, n)
		for i := range ownerOf {
			ownerOf[i] = -1
		}
		claimed := make([]bool, n)
		for bi, b := range boxes {
			for _, m := range b.Members {
				if m >= 0 && m < n && !claimed[m] {
					claimed[m] = true
					ownerOf[m] = bi
				}
			}
		}
		uncrossLinks(res.Points, res.CellW, links, ownerOf, res.Boxes, res.Loose, res.HasLoose)
		// Link-length pass (v3.6): with crossings at zero, shorten
		// the longest links — swap a long link's endpoint with a
		// same-box device whenever the swap strictly improves the
		// (max length, total length) pair without introducing a
		// crossing or breaking a reserved-rectangle invariant.
		// Bounded and deterministic, like the uncrossing pass.
		shortenLinks(res.Points, res.CellW, links, ownerOf, res.Boxes, res.Loose, res.HasLoose)
	}
	return res
}

// shortenLinks reduces the longest link lengths by swapping device
// positions within their box, as described at the call site in
// TidyGrid. A swap is kept only when the crossing count does not
// grow and the (maximum, total) link-length pair strictly improves
// lexicographically, so the pass can never undo the uncrossing
// pass or trade a shorter maximum for a longer total.
func shortenLinks(pts []Point, cellW []float64, links [][]int, ownerOf []int, boxRects [][4]float64, looseRect [4]float64, hasLoose bool) {
	if len(links) == 0 || len(pts) < 2 {
		return
	}
	lengths := func() (maxL, total float64) {
		for _, lk := range links {
			d := math.Hypot(pts[lk[0]].X-pts[lk[1]].X, pts[lk[0]].Y-pts[lk[1]].Y)
			total += d
			if d > maxL {
				maxL = d
			}
		}
		return
	}
	reservedInside := func(i int) bool {
		var r [4]float64
		switch {
		case ownerOf[i] >= 0:
			r = boxRects[ownerOf[i]]
		case hasLoose:
			r = looseRect
		default:
			return true
		}
		p := pts[i]
		return p.X-p.FW/2 >= r[0]-0.5 && p.X+p.FW/2 <= r[2]+0.5 &&
			p.Y-p.FH >= r[1]-0.5 && p.Y+gridMarkerTop <= r[3]+0.5
	}
	reservedClash := func(i int) bool {
		a := pts[i]
		for j := range pts {
			if j == i {
				continue
			}
			b := pts[j]
			if (a.FW+b.FW)/2-math.Abs(a.X-b.X) <= 0 {
				continue
			}
			aTop, aBot := a.Y+gridMarkerTop, a.Y-a.FH
			bTop, bBot := b.Y+gridMarkerTop, b.Y-b.FH
			if math.Min(aTop, bTop)-math.Max(aBot, bBot) > 0 {
				return true
			}
		}
		return false
	}
	swapPos := func(a, b int) {
		pts[a].X, pts[b].X = pts[b].X, pts[a].X
		pts[a].Y, pts[b].Y = pts[b].Y, pts[a].Y
		if a < len(cellW) && b < len(cellW) {
			cellW[a], cellW[b] = cellW[b], cellW[a]
		}
	}
	crossings := linkCrossCount(pts, links)
	curMax, curTotal := lengths()
	for pass := 0; pass < 16; pass++ {
		// Candidate endpoints: the ends of the currently longest
		// links, longest first (ties by link order).
		order := make([]int, len(links))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			la := math.Hypot(pts[links[order[a]][0]].X-pts[links[order[a]][1]].X, pts[links[order[a]][0]].Y-pts[links[order[a]][1]].Y)
			lb := math.Hypot(pts[links[order[b]][0]].X-pts[links[order[b]][1]].X, pts[links[order[b]][0]].Y-pts[links[order[b]][1]].Y)
			return la > lb
		})
		improved := false
		tried := make([]bool, len(pts))
		for _, li := range order[:min(12, len(order))] {
			for _, a := range links[li] {
				if tried[a] {
					continue
				}
				tried[a] = true
				for b := 0; b < len(pts); b++ {
					if b == a || ownerOf[b] != ownerOf[a] {
						continue
					}
					swapPos(a, b)
					newMax, newTotal := lengths()
					better := newMax < curMax-0.01 || (newMax <= curMax+0.01 && newTotal < curTotal-0.5)
					ok := better && linkCrossCount(pts, links) <= crossings &&
						reservedInside(a) && reservedInside(b) && !reservedClash(a) && !reservedClash(b)
					if ok {
						curMax, curTotal = newMax, newTotal
						crossings = linkCrossCount(pts, links)
						improved = true
						break // device a found its better cell; move on
					}
					swapPos(a, b)
				}
			}
		}
		if !improved {
			return
		}
	}
}

// orientSign returns the sign of the cross product (b-a) x (c-a):
// +1 when c lies left of the directed line a->b, -1 when right,
// 0 when collinear.
func orientSign(ax, ay, bx, by, cx, cy float64) float64 {
	v := (bx-ax)*(cy-ay) - (by-ay)*(cx-ax)
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// segProperCross reports whether segments a-b and c-d cross at a
// point strictly inside both. Shared endpoints, endpoint touches
// and collinear overlaps do not count: links fanning out of one
// device are meant to meet there.
func segProperCross(a, b, c, d Point) bool {
	d1 := orientSign(c.X, c.Y, d.X, d.Y, a.X, a.Y)
	d2 := orientSign(c.X, c.Y, d.X, d.Y, b.X, b.Y)
	d3 := orientSign(a.X, a.Y, b.X, b.Y, c.X, c.Y)
	d4 := orientSign(a.X, a.Y, b.X, b.Y, d.X, d.Y)
	return d1*d2 < 0 && d3*d4 < 0
}

// linkCrossCount counts the proper crossings among all link
// pairs with four distinct endpoints.
func linkCrossCount(pts []Point, links [][]int) int {
	n := 0
	for i := 0; i < len(links); i++ {
		for j := i + 1; j < len(links); j++ {
			a, b := links[i], links[j]
			if a[0] == b[0] || a[0] == b[1] || a[1] == b[0] || a[1] == b[1] {
				continue
			}
			if segProperCross(pts[a[0]], pts[a[1]], pts[b[0]], pts[b[1]]) {
				n++
			}
		}
	}
	return n
}

// uncrossLinks removes link-link crossings by swapping device
// positions within their box, as described at the call site in
// TidyGrid. pts and cellW are modified in place; a swap is kept
// only when the crossing count strictly falls and both swapped
// devices stay valid (reserved rectangle inside the owner's
// drawn rectangle, no reserved-rectangle overlap with any
// device). Passes repeat while they keep improving, so the
// result is deterministic: candidates are visited in link and
// index order and the first improving swap wins.
func uncrossLinks(pts []Point, cellW []float64, links [][]int, ownerOf []int, boxRects [][4]float64, looseRect [4]float64, hasLoose bool) {
	// incident[link] membership per point, for incremental
	// crossing counts: only pairs touching a swapped point can
	// change, so a candidate's effect is measured on those.
	inSet := func(set map[int]bool, a, b int) int {
		n := 0
		for i := 0; i < len(links); i++ {
			for j := i + 1; j < len(links); j++ {
				if !set[i] && !set[j] {
					continue
				}
				p, q := links[i], links[j]
				if p[0] == q[0] || p[0] == q[1] || p[1] == q[0] || p[1] == q[1] {
					continue
				}
				if segProperCross(pts[p[0]], pts[p[1]], pts[q[0]], pts[q[1]]) {
					n++
				}
			}
		}
		return n
	}
	reservedInside := func(i int) bool {
		var r [4]float64
		switch {
		case ownerOf[i] >= 0:
			r = boxRects[ownerOf[i]]
		case hasLoose:
			r = looseRect
		default:
			return true
		}
		p := pts[i]
		return p.X-p.FW/2 >= r[0]-0.5 && p.X+p.FW/2 <= r[2]+0.5 &&
			p.Y-p.FH >= r[1]-0.5 && p.Y+gridMarkerTop <= r[3]+0.5
	}
	reservedClash := func(i int) bool {
		a := pts[i]
		for j := range pts {
			if j == i {
				continue
			}
			b := pts[j]
			if (a.FW+b.FW)/2-math.Abs(a.X-b.X) <= 0 {
				continue
			}
			aTop, aBot := a.Y+gridMarkerTop, a.Y-a.FH
			bTop, bBot := b.Y+gridMarkerTop, b.Y-b.FH
			if math.Min(aTop, bTop)-math.Max(aBot, bBot) > 0 {
				return true
			}
		}
		return false
	}
	swapPos := func(a, b int) {
		pts[a].X, pts[b].X = pts[b].X, pts[a].X
		pts[a].Y, pts[b].Y = pts[b].Y, pts[a].Y
		if a < len(cellW) && b < len(cellW) {
			cellW[a], cellW[b] = cellW[b], cellW[a]
		}
	}
	cur := linkCrossCount(pts, links)
	for pass := 0; pass < 24 && cur > 0; pass++ {
		// Seed candidates: the endpoints of the links that take
		// part in a crossing, in link order.
		var seeds []int
		seenSeed := make([]bool, len(pts))
		for i := 0; i < len(links); i++ {
			for j := i + 1; j < len(links); j++ {
				p, q := links[i], links[j]
				if p[0] == q[0] || p[0] == q[1] || p[1] == q[0] || p[1] == q[1] {
					continue
				}
				if !segProperCross(pts[p[0]], pts[p[1]], pts[q[0]], pts[q[1]]) {
					continue
				}
				for _, e := range []int{p[0], p[1], q[0], q[1]} {
					if !seenSeed[e] {
						seenSeed[e] = true
						seeds = append(seeds, e)
					}
				}
			}
		}
		if len(seeds) == 0 {
			return
		}
		improved := false
		for _, a := range seeds {
			// Link indices incident to a or to a swap partner of
			// a change with the partner, so the incident set is
			// rebuilt per partner.
			for b := 0; b < len(pts); b++ {
				if b == a || ownerOf[b] != ownerOf[a] {
					continue
				}
				set := make(map[int]bool)
				for li, lk := range links {
					if lk[0] == a || lk[1] == a || lk[0] == b || lk[1] == b {
						set[li] = true
					}
				}
				before := inSet(set, a, b)
				if before == 0 {
					continue
				}
				swapPos(a, b)
				after := inSet(set, a, b)
				ok := after < before && reservedInside(a) && reservedInside(b) &&
					!reservedClash(a) && !reservedClash(b)
				if ok {
					cur -= before - after
					improved = true
				} else {
					swapPos(a, b)
				}
			}
		}
		if !improved {
			return
		}
	}
}

// gridSceneFit scores a grid layout against a canvas: the fit
// ratio of the whole scene — drawn boxes plus every point's
// reserved rectangle — against the canvas size. Higher is
// better; 1 means the scene exactly fills the canvas.
func gridSceneFit(res GridResult, boxW, boxH float64) float64 {
	x1, y1 := math.Inf(1), math.Inf(1)
	x2, y2 := math.Inf(-1), math.Inf(-1)
	eat := func(a, b, c, d float64) {
		x1 = math.Min(x1, a)
		y1 = math.Min(y1, b)
		x2 = math.Max(x2, c)
		y2 = math.Max(y2, d)
	}
	for _, b := range res.Boxes {
		eat(b[0], b[1], b[2], b[3])
	}
	if res.HasLoose {
		eat(res.Loose[0], res.Loose[1], res.Loose[2], res.Loose[3])
	}
	for _, p := range res.Points {
		eat(p.X-p.FW/2, p.Y-p.FH, p.X+p.FW/2, p.Y+gridMarkerTop)
	}
	w, h := x2-x1, y2-y1
	if !(w > 0) || !(h > 0) {
		return 0
	}
	return math.Min(boxW/w, boxH/h)
}

func normEnv(e [4]float64) [4]float64 {
	return [4]float64{math.Min(e[0], e[2]), math.Min(e[1], e[3]), math.Max(e[0], e[2]), math.Max(e[1], e[3])}
}

// sizeNode computes a node's block size and the layout of
// everything inside it, relative to the node's own rectangle
// centre: its own grid (from its members) and its child boxes
// are placed at their stored centres and pushed apart until no
// two blocks overlap.
func sizeNode(nd *gNode, pts []Point, boxes []GridBox, minDX, minDY float64, opts Options, overrides map[int]int, ctx *gridCtx) {
	for _, ch := range nd.children {
		sizeNode(ch, pts, boxes, minDX, minDY, opts, overrides, ctx)
	}
	// Own grid metrics.
	if len(nd.members) > 0 {
		nd.maxW, nd.maxH = 0, 0
		for _, m := range nd.members {
			if pts[m].FW > nd.maxW {
				nd.maxW = pts[m].FW
			}
			if h := gridMarkerTop + pts[m].FH; h > nd.maxH {
				nd.maxH = h
			}
		}
		nd.pitchX = math.Max(minDX, nd.maxW+gridGapX)
		nd.pitchY = math.Max(minDY, nd.maxH+gridGapY)
		// Column count from the stored aspect: the grid's
		// width/height ratio should approach the aspect of the
		// shape the members were stored in (the box envelope, or
		// the members' own stored bounding box for the loose
		// group), so the arranged box keeps the drawing's
		// proportions instead of inventing new ones.
		aspect := 0.0
		if nd.box >= 0 {
			env := normEnv(boxes[nd.box].Env)
			if env[2]-env[0] > 0 && env[3]-env[1] > 0 {
				aspect = (env[2] - env[0]) / (env[3] - env[1])
			}
		} else {
			var x1, y1, x2, y2 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, m := range nd.members {
				x1 = math.Min(x1, pts[m].X)
				x2 = math.Max(x2, pts[m].X)
				y1 = math.Min(y1, pts[m].Y)
				y2 = math.Max(y2, pts[m].Y)
			}
			if x2-x1 > 0 && y2-y1 > 0 {
				aspect = (x2 - x1) / (y2 - y1)
			}
		}
		if aspect <= 0 {
			aspect = opts.Aspect
		}
		if aspect <= 0 {
			aspect = 1
		}
		n := float64(len(nd.members))
		cols := int(math.Round(math.Sqrt(n * nd.pitchY * aspect / nd.pitchX)))
		if cols < 1 {
			cols = 1
		}
		if cols > len(nd.members) {
			cols = len(nd.members)
		}
		nd.cols = cols
		// Keep the block's own aspect inside the band a screen
		// can hold (see gridBlockAspectMin/Max): nudge the
		// column count the fewest steps that bring it in. Only
		// the grid's shape inside the box changes — which box
		// sits where is untouched.
		shape := func() float64 {
			nd.rows = (len(nd.members) + nd.cols - 1) / nd.cols
			nd.ownW = float64(nd.cols-1)*nd.pitchX + nd.maxW
			nd.ownH = float64(nd.rows-1)*nd.pitchY + nd.maxH
			return nd.ownW / nd.ownH
		}
		for nd.cols < len(nd.members) && shape() < gridBlockAspectMin {
			nd.cols++
		}
		for nd.cols > 1 && shape() > gridBlockAspectMax {
			nd.cols--
		}
		shape()
		// A column override from the scene-fit optimisation
		// wins over the aspect-derived count.
		if ov, ok := overrides[nd.box]; ok && ov >= 1 && ov <= len(nd.members) {
			nd.cols = ov
			shape()
		}
		nd.rows = (len(nd.members) + nd.cols - 1) / nd.cols
		// Cell assignment: members with links into other boxes
		// take the cells on the side facing the partner box;
		// everyone else keeps the breadth-first fill order (see
		// assignCells).
		nd.assign = assignCells(nd, ctx)
		// Per-column widths: each column is exactly as wide as
		// its widest reserved rectangle, so a column of narrow
		// labels no longer pays for the widest label in the box.
		// Adjacent column centres still keep the caller's
		// minimum separation: a sweep widens the two columns of
		// any pair that falls short (widening a column only ever
		// increases the separation to its other neighbour, so
		// one left-to-right sweep settles every pair).
		nd.colW = make([]float64, nd.cols)
		for f, mp := range nd.assign {
			c := cellCol(nd, f)
			if w := pts[nd.members[mp]].FW; w > nd.colW[c] {
				nd.colW[c] = w
			}
		}
		for c := 0; c+1 < nd.cols; c++ {
			dist := (nd.colW[c]+nd.colW[c+1])/2 + gridGapX
			if dist < minDX {
				// Widening both columns by the full deficit
				// raises their centre distance by exactly the
				// deficit: (a+g + b+g)/2 = (a+b)/2 + g.
				grow := minDX - dist
				nd.colW[c] += grow
				nd.colW[c+1] += grow
			}
		}
		nd.colX = make([]float64, nd.cols)
		start := 0.0
		for c := range nd.colW {
			nd.colX[c] = start + nd.colW[c]/2
			start += nd.colW[c] + gridGapX
		}
		nd.ownW = start - gridGapX
		nd.ownH = float64(nd.rows-1)*nd.pitchY + nd.maxH
	}
	// Blocks inside this node: child boxes plus the own grid.
	type blk struct {
		child int // index into nd.children, -1 = own grid
		b     gBlock
	}
	var blks []blk
	for ci, ch := range nd.children {
		blks = append(blks, blk{ci, gBlock{w: ch.w, h: ch.h, cx: ch.scx, cy: ch.scy}})
	}
	if len(nd.members) > 0 {
		var cx, cy float64
		for _, m := range nd.members {
			cx += pts[m].X
			cy += pts[m].Y
		}
		blks = append(blks, blk{-1, gBlock{w: nd.ownW, h: nd.ownH,
			cx: cx / float64(len(nd.members)), cy: cy / float64(len(nd.members))}})
	}
	if len(blks) == 0 {
		// A box with nothing in it keeps its stored size.
		if nd.box >= 0 {
			env := normEnv(boxes[nd.box].Env)
			nd.w = math.Max(env[2]-env[0], 1)
			nd.h = math.Max(env[3]-env[1], 1)
		}
		return
	}
	bs := make([]gBlock, len(blks))
	for i, b := range blks {
		bs[i] = b.b
	}
	placeBlocks(bs)
	// The node's rectangle wraps the placed blocks with
	// gridBoxPad of clear space on every side. The loose group
	// takes the same pad: it reports its rectangle (see
	// GridResult.Loose) and the renderer frames it like a box,
	// so its devices need the same border clearance.
	pad := gridBoxPad
	x1, y1, x2, y2 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, b := range bs {
		x1 = math.Min(x1, b.cx-b.w/2)
		x2 = math.Max(x2, b.cx+b.w/2)
		y1 = math.Min(y1, b.cy-b.h/2)
		y2 = math.Max(y2, b.cy+b.h/2)
	}
	nd.w = (x2 - x1) + 2*pad
	nd.h = (y2 - y1) + 2*pad
	ccx, ccy := (x1+x2)/2, (y1+y2)/2 // content centre; rect centre = content centre (pad is symmetric)
	nd.childOffX = make([]float64, len(nd.children))
	nd.childOffY = make([]float64, len(nd.children))
	for i, b := range blks {
		if b.child >= 0 {
			nd.childOffX[b.child] = bs[i].cx - ccx
			nd.childOffY[b.child] = bs[i].cy - ccy
		} else {
			nd.ownOffX = bs[i].cx - ccx
			nd.ownOffY = bs[i].cy - ccy
		}
	}
}

// cellCol maps a fill index to its grid column under the
// serpentine fill: even rows run left to right, odd rows right
// to left, so consecutive fill indices stay in neighbouring
// cells.
func cellCol(nd *gNode, f int) int {
	r, t := f/nd.cols, f%nd.cols
	if r%2 == 1 {
		return nd.cols - 1 - t
	}
	return t
}

// cellAllot is the horizontal room a cell's label budget is
// based on: the distance to the nearer neighbouring column
// centre, or — at a block edge — twice the run to the block's
// edge (a centred label may use the whole run on that side),
// whichever is smaller.
func cellAllot(nd *gNode, c int) float64 {
	l, r := 2*nd.colX[c], 2*(nd.ownW-nd.colX[c])
	if c > 0 {
		l = nd.colX[c] - nd.colX[c-1]
	}
	if c+1 < nd.cols {
		r = nd.colX[c+1] - nd.colX[c]
	}
	return math.Min(l, r)
}

// assignCells decides which member sits in which grid cell. In
// the base order — the node's members are already sorted by
// their breadth-first rank over the link graph — fill index f
// simply takes member f, which puts link partners in
// neighbouring cells inside a box. A member whose links cross
// into another box is the exception: it is assigned first, to
// the still-free cell (on the uniform grid) closest to the aim
// point — the cell on the side facing the partner, at the
// partner's height — so the cross-box link leaves from the
// facing edge instead of running diagonally across the grid.
// The aim
// point is the partner box's centre (its average, for several
// partner boxes): the stored centre in the first pass, the
// first pass's drawn centre in the refinement pass (see
// TidyGrid). A partner in the same box is ignored (the base
// order already handles it). Cross members are placed in rank order, then
// every remaining member takes the remaining cells in fill
// order, preserving the base sequence. The result is a
// permutation: assign[f] is the member index (into nd.members)
// placed at fill index f.
func assignCells(nd *gNode, ctx *gridCtx) []int {
	n := len(nd.members)
	assign := make([]int, n)
	for i := range assign {
		assign[i] = i
	}
	if n == 0 || ctx == nil {
		return assign
	}
	own := ctx.centre[nd.box]
	type crosser struct {
		pos    int
		tx, ty float64 // aim point: average partner-box centre
	}
	var crossers []crosser
	isCross := make([]bool, n)
	for pos, m := range nd.members {
		var sx, sy float64
		cnt := 0
		for _, q := range ctx.adj[m] {
			ob := ctx.owner[q]
			if ob == nd.box {
				continue
			}
			c, ok := ctx.centre[ob]
			if !ok {
				continue
			}
			sx, sy, cnt = sx+c[0], sy+c[1], cnt+1
		}
		if cnt > 0 {
			isCross[pos] = true
			crossers = append(crossers, crosser{pos, sx / float64(cnt), sy / float64(cnt)})
		}
	}
	if len(crossers) == 0 {
		return assign
	}
	uniW := float64(nd.cols-1)*nd.pitchX + nd.maxW
	uniH := float64(nd.rows-1)*nd.pitchY + nd.maxH
	cellRel := func(f int) (float64, float64) {
		r := f / nd.cols
		c := cellCol(nd, f)
		return -uniW/2 + nd.maxW/2 + float64(c)*nd.pitchX,
			uniH/2 - gridMarkerTop - float64(r)*nd.pitchY
	}
	usedCell := make([]bool, n)
	usedPos := make([]bool, n)
	for _, cm := range crossers {
		// The still-free cell closest to the aim point wins.
		// (Projection onto the aim direction was tried first and
		// rejected: when the aim is nearly horizontal, a few
		// units of vertical tilt make a far corner cell beat the
		// cell actually nearest the partner box.)
		best, bestScore := -1, math.Inf(-1)
		for f := 0; f < n; f++ {
			if usedCell[f] {
				continue
			}
			x, y := cellRel(f)
			dx, dy := own[0]+x-cm.tx, own[1]+y-cm.ty
			if s := -(dx*dx + dy*dy); s > bestScore {
				best, bestScore = f, s
			}
		}
		if best < 0 {
			continue
		}
		assign[best] = cm.pos
		usedCell[best] = true
		usedPos[cm.pos] = true
	}
	// Every member not placed above — including a crosser
	// skipped for a degenerate direction — takes the remaining
	// cells in fill order, preserving the base sequence.
	next := 0
	for pos := 0; pos < n; pos++ {
		if usedPos[pos] {
			continue
		}
		for next < n && usedCell[next] {
			next++
		}
		if next >= n {
			break
		}
		assign[next] = pos
		usedCell[next] = true
		usedPos[pos] = true
	}
	return assign
}

// placeNode assigns absolute positions: the node's own members
// onto its grid cells (serpentine by fill order), then recurses
// into child boxes.
func placeNode(nd *gNode, out []Point, cellW []float64) {
	cx := (nd.rect[0] + nd.rect[2]) / 2
	cy := (nd.rect[1] + nd.rect[3]) / 2
	if len(nd.members) > 0 {
		ocx, ocy := cx+nd.ownOffX, cy+nd.ownOffY
		left := ocx - nd.ownW/2                // the own grid block's left edge
		top := ocy + nd.ownH/2 - gridMarkerTop // first row centre (y grows up)
		for f, mp := range nd.assign {
			m := nd.members[mp]
			out[m].X = left + nd.colX[cellCol(nd, f)]
			out[m].Y = top - float64(f/nd.cols)*nd.pitchY
			cellW[m] = cellAllot(nd, cellCol(nd, f))
		}
	}
	for ci, ch := range nd.children {
		ccx, ccy := cx+nd.childOffX[ci], cy+nd.childOffY[ci]
		ch.rect = [4]float64{ccx - ch.w/2, ccy - ch.h/2, ccx + ch.w/2, ccy + ch.h/2}
		placeNode(ch, out, cellW)
	}
}

// gBlock is a rectangle being packed: a size and a centre that
// placeBlocks may move. sx, sy record the centre the block
// started from (its stored centre): the stored arrangement of a
// set of sibling boxes is semantic — carrier columns, bandwidth
// tiers, institute boxes — so placeBlocks never lets a centre
// cross the centre line of a sibling that was stored beyond it.
type gBlock struct {
	w, h   float64
	cx, cy float64
	sx, sy float64
}

// placeBlocks moves block centres (which start at their stored
// positions) until no two blocks overlap, keeping gridBoxGap of
// clear space between them. Pairs are visited in slice order and
// pushed apart along their cheaper axis, half the overlap each —
// deterministic. Every push is capped by the stored order: a
// centre may not cross the current centre line of a sibling
// whose stored centre lies beyond it on that axis, so the
// left-to-right and top-to-bottom order of the blocks is the
// stored order at every step, not just at the end. A block can
// be pushed outward and pressure escapes toward the edges of
// the set, but blocks never trade places. When the cheaper axis
// is capped, the other axis is pushed as well.
//
// Pairwise pushes can wedge in a limit cycle when several
// blocks sit exactly tight against each other (every push of
// one pair is undone by a push of the next). The wedge breaker
// is a pair of compaction sweeps (compactAxis): longest-path
// passes over the stored-order constraint graph that move whole
// chains of blocks at once, under the same caps. If even that
// leaves an overlap — no real map is known to — the blocks are
// rank-packed (rankPackBlocks): laid out on the grid of their
// stored-centre ranks, which cannot overlap and cannot reorder
// them by construction.
func placeBlocks(bs []gBlock) {
	if len(bs) < 2 {
		return
	}
	for i := range bs {
		bs[i].sx, bs[i].sy = bs[i].cx, bs[i].cy
	}
	overlaps := func() bool {
		return totalBlockOverlap(bs) > 0
	}
	sweep := func() {
		for i := 0; i < len(bs); i++ {
			for j := i + 1; j < len(bs); j++ {
				ox, oy := blockOverlapXY(bs[i], bs[j])
				if ox <= 0 || oy <= 0 {
					continue
				}
				first, second := 0, 1
				if oy < ox {
					first, second = 1, 0
				}
				pushAxis(bs, i, j, first)
				if ox, oy := blockOverlapXY(bs[i], bs[j]); ox > 0 && oy > 0 {
					pushAxis(bs, i, j, second)
				}
			}
		}
	}
	// Pairwise phase with stall detection: a sweep that does
	// not reduce the total overlap means the set is wedged, and
	// wedged sets go to the compaction sweeps instead of burning
	// rounds in a limit cycle.
	pushUntilStall := func(maxRounds int) bool {
		prev := math.Inf(1)
		stalls := 0
		for round := 0; round < maxRounds; round++ {
			sweep()
			cur := totalBlockOverlap(bs)
			if cur <= 0 {
				return true
			}
			if cur >= prev-0.0001 {
				stalls++
				if stalls >= 3 {
					return false
				}
			} else {
				stalls = 0
			}
			prev = cur
		}
		return false
	}
	if pushUntilStall(400) {
		pullToStored(bs)
		return
	}
	// Wedge breaker: compaction sweeps on both axes. Each cycle
	// must reduce the total overlap; a cycle that does not is
	// the end of what compaction can do here.
	prev := totalBlockOverlap(bs)
	for cycle := 0; cycle < 8 && prev > 0; cycle++ {
		compactAxis(bs, 0)
		compactAxis(bs, 1)
		cur := totalBlockOverlap(bs)
		if cur <= 0 {
			pullToStored(bs)
			return
		}
		if cur >= prev-0.001 {
			break
		}
		prev = cur
	}
	if pushUntilStall(400) {
		pullToStored(bs)
		return
	}
	if overlaps() {
		rankPackBlocks(bs)
		pullToStored(bs)
	}
}

// pullToStored compacts a separated block set back toward the
// stored geography: separating pushes are shared between both
// blocks of a pair and re-trigger as other pairs slide blocks
// along, so a set can end up valid but far more spread out
// than the stored arrangement needs — which inflates the
// scene and shrinks everything in it. Each block steps toward
// its stored centre, largest step first, as long as the set
// stays overlap-free and the stored-order caps hold. The
// stored position itself always satisfies the caps (it is the
// arrangement the caps were derived from), so the pull can
// only be stopped by another block, never by a cap alone.
func pullToStored(bs []gBlock) {
	for round := 0; round < 80; round++ {
		moved := false
		for i := range bs {
			for axis := 0; axis < 2; axis++ {
				cur := bs[i].cx
				target := bs[i].sx
				if axis == 1 {
					cur, target = bs[i].cy, bs[i].sy
				}
				delta := target - cur
				if math.Abs(delta) < 0.01 {
					continue
				}
				for _, frac := range []float64{1, 0.5, 0.25, 0.125} {
					step := delta * frac
					if math.Abs(step) < 0.01 {
						continue
					}
					dir := 1.0
					if step < 0 {
						dir = -1
					}
					lim := capLimit(bs, i, axis, dir)
					next := cur + step
					if step > 0 && next > lim {
						next = lim
					}
					if step < 0 && next < lim {
						next = lim
					}
					if math.Abs(next-cur) < 0.01 {
						continue
					}
					old := cur
					if axis == 0 {
						bs[i].cx = next
					} else {
						bs[i].cy = next
					}
					if totalBlockOverlap(bs) <= 0 {
						moved = true
						break
					}
					if axis == 0 {
						bs[i].cx = old
					} else {
						bs[i].cy = old
					}
				}
			}
		}
		if !moved {
			return
		}
	}
}

// totalBlockOverlap sums the pairwise block overlaps of a set.
func totalBlockOverlap(bs []gBlock) float64 {
	s := 0.0
	for i := 0; i < len(bs); i++ {
		for j := i + 1; j < len(bs); j++ {
			s += blockOverlap(bs[i], bs[j])
		}
	}
	return s
}

// axisSize reads a block's size on one axis (0 = width,
// 1 = height).
func axisSize(b gBlock, axis int) float64 {
	if axis == 0 {
		return b.w
	}
	return b.h
}

// capLimit returns the centre position block b may not pass
// when moving in direction dir along one axis: the nearest
// current centre line among siblings whose stored centre lies
// beyond b's stored centre on that axis (+Inf / -Inf when there
// is none).
func capLimit(bs []gBlock, b, axis int, dir float64) float64 {
	sb := axisStored(bs[b], axis)
	lim := math.Inf(int(dir))
	for m := range bs {
		if m == b {
			continue
		}
		sm := axisStored(bs[m], axis)
		if dir > 0 && sm > sb {
			if c := axisCentre(bs[m], axis); c < lim {
				lim = c
			}
		}
		if dir < 0 && sm < sb {
			if c := axisCentre(bs[m], axis); c > lim {
				lim = c
			}
		}
	}
	return lim
}

// compactAxis runs two compaction sweeps along one axis: a
// forward sweep in stored-centre order that pushes each block
// just far enough along +dir to clear every stored-before
// sibling whose span on the other axis overlaps its own, then a
// backward sweep that pulls blocks back along -dir under the
// same constraints, settling the set. Blocks are applied one at
// a time in stored order and every position is clamped by
// capLimit, so no centre ever crosses a sibling stored beyond
// it — the stored order survives the sweeps exactly as it
// survives the pairwise pushes. A constraint the cap leaves
// unsatisfied stays for the other axis (whose bands then see a
// different overlap) or for the pairwise polish.
func compactAxis(bs []gBlock, axis int) {
	order := make([]int, len(bs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		sa, sb := axisStored(bs[ia], axis), axisStored(bs[ib], axis)
		if sa != sb {
			return sa < sb
		}
		return ia < ib
	})
	need := func(i, j int) float64 {
		return (axisSize(bs[i], axis)+axisSize(bs[j], axis))/2 + gridBoxGap
	}
	band := func(i, j int) bool {
		// The pair constrains this axis only while their spans
		// on the other axis overlap (with the box gap).
		o := 1 - axis
		return math.Abs(axisCentre(bs[i], o)-axisCentre(bs[j], o)) <
			(axisSize(bs[i], o)+axisSize(bs[j], o))/2+gridBoxGap-0.01
	}
	for k := 1; k < len(order); k++ {
		i := order[k]
		target := axisCentre(bs[i], axis)
		for m := 0; m < k; m++ {
			j := order[m]
			if band(i, j) {
				if v := axisCentre(bs[j], axis) + need(i, j); v > target {
					target = v
				}
			}
		}
		if hi := capLimit(bs, i, axis, 1); target > hi {
			target = hi
		}
		axisSet(&bs[i], axis, target)
	}
	for k := len(order) - 2; k >= 0; k-- {
		i := order[k]
		target := axisCentre(bs[i], axis)
		for m := k + 1; m < len(order); m++ {
			j := order[m]
			if band(i, j) {
				if v := axisCentre(bs[j], axis) - need(i, j); v < target {
					target = v
				}
			}
		}
		if lo := capLimit(bs, i, axis, -1); target < lo {
			target = lo
		}
		axisSet(&bs[i], axis, target)
	}
}

// axisCentre and axisStored read a block's current and stored
// centre on one axis (0 = x, 1 = y); axisSet writes the current
// centre.
func axisCentre(b gBlock, axis int) float64 {
	if axis == 0 {
		return b.cx
	}
	return b.cy
}

func axisStored(b gBlock, axis int) float64 {
	if axis == 0 {
		return b.sx
	}
	return b.sy
}

func axisSet(b *gBlock, axis int, v float64) {
	if axis == 0 {
		b.cx = v
	} else {
		b.cy = v
	}
}

// blockCap returns how far block b may move in direction dir
// (+1 or -1) along one axis before its centre would cross the
// current centre line of a sibling whose stored centre lies
// beyond b's stored centre on that axis. Siblings stored at the
// same centre coordinate do not constrain the move (their order
// on that axis is a tie, either way is the stored order).
func blockCap(bs []gBlock, b, axis int, dir float64) float64 {
	cur := axisCentre(bs[b], axis)
	lim := capLimit(bs, b, axis, dir)
	if dir > 0 {
		if d := lim - cur; d > 0 {
			return d
		}
		return 0
	}
	if d := cur - lim; d > 0 {
		return d
	}
	return 0
}

// pushAxis pushes blocks i and j apart along one axis by their
// current overlap on it, each moving half, under the stored-
// order caps (blockCap). A block whose half is capped simply
// moves less; the other block's movement still widens the gap.
// Returns the total distance moved.
func pushAxis(bs []gBlock, i, j, axis int) float64 {
	ox, oy := blockOverlapXY(bs[i], bs[j])
	amt := ox
	if axis == 1 {
		amt = oy
	}
	if amt <= 0 {
		return 0
	}
	ci, cj := axisCentre(bs[i], axis), axisCentre(bs[j], axis)
	s := 1.0
	if cj < ci {
		s = -1
	}
	mi := blockCap(bs, i, axis, -s)
	if mi > amt/2 {
		mi = amt / 2
	}
	mj := blockCap(bs, j, axis, s)
	if mj > amt/2 {
		mj = amt / 2
	}
	if mi <= 0 && mj <= 0 {
		return 0
	}
	axisSet(&bs[i], axis, ci-s*mi)
	axisSet(&bs[j], axis, cj+s*mj)
	return mi + mj
}

// blockOverlapXY returns how far two blocks (inflated by
// gridBoxGap) overlap on each axis; both must be positive for a
// real overlap. Overlaps under a hundredth of a unit are float
// residue from pushes that landed exactly on the boundary and
// read as no overlap — without the tolerance the pair is pushed
// forever without ever separating.
func blockOverlapXY(a, b gBlock) (float64, float64) {
	ox := (a.w+b.w)/2 + gridBoxGap - math.Abs(b.cx-a.cx)
	oy := (a.h+b.h)/2 + gridBoxGap - math.Abs(b.cy-a.cy)
	if ox < 0.01 {
		ox = 0
	}
	if oy < 0.01 {
		oy = 0
	}
	return ox, oy
}

func blockOverlap(a, b gBlock) float64 {
	ox, oy := blockOverlapXY(a, b)
	if ox > 0 && oy > 0 {
		return math.Min(ox, oy)
	}
	return 0
}

// rankPackBlocks is the fallback packing: it lays the blocks
// on a grid whose columns are the blocks' stored-centre ranks
// left to right and whose rows are their stored-centre ranks
// top to bottom, each column as wide as its widest block and
// each row as tall as its tallest. A block's column rank comes
// from a strict ordering, so two blocks never share a column or
// a row rank pair, columns and rows tile without overlap, and
// the stored left-to-right and top-to-bottom order holds by
// construction — the fallback can be plainer than the pushed
// arrangement, never wronger. Centres end in a local frame
// starting at the origin; callers recompute bounding boxes from
// the centres, so the frame itself does not matter.
func rankPackBlocks(bs []gBlock) {
	rankOf := func(axis int) []int {
		order := make([]int, len(bs))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool {
			ia, ib := order[a], order[b]
			sa, sb := axisStored(bs[ia], axis), axisStored(bs[ib], axis)
			if sa != sb {
				return sa < sb
			}
			return ia < ib
		})
		rank := make([]int, len(bs))
		for pos, i := range order {
			rank[i] = pos
		}
		return rank
	}
	xRank, yRank := rankOf(0), rankOf(1)
	colW := make([]float64, len(bs))
	rowH := make([]float64, len(bs))
	for i, b := range bs {
		if b.w > colW[xRank[i]] {
			colW[xRank[i]] = b.w
		}
		if b.h > rowH[yRank[i]] {
			rowH[yRank[i]] = b.h
		}
	}
	colX := make([]float64, len(bs))
	x := 0.0
	for c := range colW {
		colX[c] = x
		x += colW[c] + gridBoxGap
	}
	rowTop := make([]float64, len(bs))
	top := 0.0
	for r := len(bs) - 1; r >= 0; r-- {
		rowTop[r] = top
		top -= rowH[r] + gridBoxGap
	}
	for i := range bs {
		bs[i].cx = colX[xRank[i]] + colW[xRank[i]]/2
		bs[i].cy = rowTop[yRank[i]] - rowH[yRank[i]]/2
	}
}

// GridViolations counts the point pairs whose grid reserved
// rectangles overlap. The grid footprint is asymmetric — the
// label stack hangs below the marker — so this is a different
// measure from Violations (the heuristic pipeline's symmetric
// separation check): two points in adjacent rows across a region
// border can be closer than the heuristic minima on an axis
// while their reserved rectangles still clear each other, which
// is the property the grid layout actually guarantees.
func GridViolations(pts []Point) int {
	n := 0
	for i := 0; i < len(pts); i++ {
		for j := i + 1; j < len(pts); j++ {
			a, b := pts[i], pts[j]
			ox := (a.FW+b.FW)/2 - math.Abs(a.X-b.X)
			if ox <= 0 {
				continue
			}
			// Reserved vertical span: FH below the centre,
			// gridMarkerTop above it.
			aTop, aBot := a.Y+gridMarkerTop, a.Y-a.FH
			bTop, bBot := b.Y+gridMarkerTop, b.Y-b.FH
			if math.Min(aTop, bTop)-math.Max(aBot, bBot) > 0 {
				n++
			}
		}
	}
	return n
}
