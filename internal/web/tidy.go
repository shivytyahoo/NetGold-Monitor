package web

// The tidy view endpoint (v3.4). The computation itself lives in
// internal/layout as a pure function; this handler is only the
// transport: validate the request, run the layout, return the new
// positions. Nothing is stored — a tidy arrangement is a view over
// the stored positions, never a change to them — so the endpoint is
// also open to read-only (viewer) sessions.
//
// v3.4.2: the request may carry snap groups (point IDs that belong
// together, e.g. the devices of one map region) plus snap
// tolerances; the layout then runs its grid-snap pass so rows and
// columns come out ruler-straight. Requests without groups keep
// the plain v3.4 behaviour exactly.
//
// v3.4.3: points may carry individual footprints (fw/fh — the
// marker plus its label stack, in point units); pairs then keep
// the footprint distance, so the arrangement reserves each
// label's space instead of leaving labels to overlap markers.
//
// v3.4.4: the request may also carry the drawing's link pairs
// (point IDs); the layout is then link-aware — connected devices
// are arranged next to each other, so a link reads as a short hop
// between neighbours instead of a line spanning the whole map.
//
// Grid mode: a request with mode "grid" takes the grid-first
// layout instead — devices are placed on a per-region grid of
// cells that reserve the marker plus the label stack below it,
// so nothing can overlap by construction. Grid requests carry
// the region boxes (member point IDs, stored envelope, parent
// box) and use grid footprint semantics: fw is the label pill's
// width and fh is how far the label stack reaches below the
// marker centre. The response adds the drawn rectangle of every
// box and each point's cell width. Requests without a mode keep
// the behaviour they always had.

import (
	"math"
	"net/http"

	"netgold/internal/layout"
)

type tidyRequest struct {
	Points   []layout.Point `json:"points"`
	MinDX    float64        `json:"min_dx"`
	MinDY    float64        `json:"min_dy"`
	Groups   [][]int64      `json:"groups,omitempty"`
	SnapTolX float64        `json:"snap_tol_x,omitempty"`
	SnapTolY float64        `json:"snap_tol_y,omitempty"`
	// Aspect is the target width/height ratio of the canvas the
	// arrangement will be shown on (v3.4.3); the layout steers
	// its separation pushes so the arranged span approaches it.
	// Zero (or absent) disables the steering.
	Aspect float64 `json:"aspect,omitempty"`
	// BoxW and BoxH are the canvas size in point units
	// (v3.4.3); when the relaxed arrangement overflows the box,
	// the layout also tries a shelf-packed arrangement and keeps
	// the better fit. Zero disables the packing fallback.
	BoxW float64 `json:"box_w,omitempty"`
	BoxH float64 `json:"box_h,omitempty"`
	// Links are the drawing's link pairs as point IDs (v3.4.4);
	// the layout pulls linked points toward adjacency. Unknown
	// IDs, duplicates and self-links are ignored.
	Links [][2]int64 `json:"links,omitempty"`
	// Mode selects the layout engine: "" keeps the classic
	// relaxation pipeline; "grid" takes the grid-first layout.
	Mode string `json:"mode,omitempty"`
	// Boxes are the drawing's region boxes for grid mode: the
	// member point IDs (devices whose smallest containing square
	// the box is), the stored envelope in request units, and the
	// parent box's ID (0 = top level). Points in no box form the
	// canvas-level group.
	Boxes []tidyBoxRequest `json:"boxes,omitempty"`
}

type tidyBoxRequest struct {
	ID     int64      `json:"id"`
	IDs    []int64    `json:"ids"`
	Env    [4]float64 `json:"env"`
	Parent int64      `json:"parent"`
}

func (s *Server) tidyLayout(w http.ResponseWriter, r *http.Request) {
	var req tidyRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Points) > 5000 {
		errJSON(w, 400, "too many points in one layout request")
		return
	}
	if req.MinDX < 1 || req.MinDY < 1 || req.MinDX > 100000 || req.MinDY > 100000 {
		errJSON(w, 400, "min_dx and min_dy must be between 1 and 100000")
		return
	}
	// Per-point footprints (v3.4.3) are optional; when present
	// they must be sane. A negative or absurd footprint would
	// corrupt the separation math, so reject the request.
	for _, p := range req.Points {
		if p.FW < 0 || p.FH < 0 || p.FW > 100000 || p.FH > 100000 {
			errJSON(w, 400, "point footprints must be between 0 and 100000")
			return
		}
	}
	aspect := req.Aspect
	if aspect < 0 || aspect > 100 {
		aspect = 0
	}
	opts := layout.Options{Aspect: aspect}
	if req.BoxW > 0 && req.BoxH > 0 && req.BoxW <= 10000000 && req.BoxH <= 10000000 {
		opts.BoxW, opts.BoxH = req.BoxW, req.BoxH
	}
	// Translate point IDs to indices once; groups and links both
	// speak in IDs on the wire and in indices inside the layout.
	idxOf := make(map[int64]int, len(req.Points))
	for i, p := range req.Points {
		if _, ok := idxOf[p.ID]; !ok {
			idxOf[p.ID] = i
		}
	}
	if len(req.Links) > 20000 {
		errJSON(w, 400, "too many links in one layout request")
		return
	}
	seenLink := make(map[[2]int]bool, len(req.Links))
	for _, lk := range req.Links {
		ia, okA := idxOf[lk[0]]
		ib, okB := idxOf[lk[1]]
		if !okA || !okB || ia == ib {
			continue
		}
		key := [2]int{ia, ib}
		if ia > ib {
			key = [2]int{ib, ia}
		}
		if seenLink[key] {
			continue
		}
		seenLink[key] = true
		opts.Links = append(opts.Links, []int{ia, ib})
	}
	if req.Mode == "grid" {
		s.tidyGrid(w, req, idxOf, opts)
		return
	}
	if len(req.Groups) == 0 {
		pts, seeded := layout.TidyDiag(req.Points, req.MinDX, req.MinDY, opts)
		writeJSON(w, 200, map[string]any{"points": pts, "violations": layout.Violations(pts, req.MinDX, req.MinDY), "seeded": seeded})
		return
	}
	if req.SnapTolX <= 0 || req.SnapTolY <= 0 || req.SnapTolX > 100000 || req.SnapTolY > 100000 {
		errJSON(w, 400, "snap tolerances must be between 0 and 100000 when groups are given")
		return
	}
	// Translate group ID lists into point indices; unknown IDs are
	// ignored and a point joins the first group that names it (the
	// layout package applies the same rule to leftovers).
	groups := make([][]int, 0, len(req.Groups))
	for _, g := range req.Groups {
		idxs := make([]int, 0, len(g))
		for _, id := range g {
			if i, ok := idxOf[id]; ok {
				idxs = append(idxs, i)
			}
		}
		if len(idxs) > 0 {
			groups = append(groups, idxs)
		}
	}
	pts, seeded := layout.TidySnapDiag(req.Points, req.MinDX, req.MinDY, groups, req.SnapTolX, req.SnapTolY, opts)
	writeJSON(w, 200, map[string]any{"points": pts, "violations": layout.Violations(pts, req.MinDX, req.MinDY), "seeded": seeded})
}

// tidyGrid runs the grid-first layout for a mode:"grid" request
// and writes its response: the arranged points, the drawn
// rectangle of every box the request carried, and each point's
// cell width (parallel to points) for the renderer's label
// budgets.
func (s *Server) tidyGrid(w http.ResponseWriter, req tidyRequest, idxOf map[int64]int, opts layout.Options) {
	if len(req.Boxes) > 5000 {
		errJSON(w, 400, "too many boxes in one layout request")
		return
	}
	boxIdx := make(map[int64]int, len(req.Boxes))
	for i, b := range req.Boxes {
		for _, v := range b.Env {
			if math.IsNaN(v) || math.IsInf(v, 0) || v > 1e8 || v < -1e8 {
				errJSON(w, 400, "box envelopes must be finite")
				return
			}
		}
		if _, ok := boxIdx[b.ID]; !ok {
			boxIdx[b.ID] = i
		}
	}
	gboxes := make([]layout.GridBox, len(req.Boxes))
	for i, b := range req.Boxes {
		gb := layout.GridBox{ID: b.ID, Env: b.Env, Parent: -1}
		for _, id := range b.IDs {
			if ix, ok := idxOf[id]; ok {
				gb.Members = append(gb.Members, ix)
			}
		}
		if b.Parent != 0 {
			if pi, ok := boxIdx[b.Parent]; ok && pi != i {
				gb.Parent = pi
			}
		}
		gboxes[i] = gb
	}
	res := layout.TidyGrid(req.Points, req.MinDX, req.MinDY, gboxes, opts.Links, opts)
	boxes := make([]map[string]any, len(gboxes))
	for i, gb := range gboxes {
		boxes[i] = map[string]any{"id": gb.ID, "env": res.Boxes[i]}
	}
	// The loose group's drawn rectangle (points in no box), so
	// the client can frame them like a region; null when every
	// point belongs to a box.
	var loose any
	if res.HasLoose {
		loose = map[string]any{"env": res.Loose}
	}
	writeJSON(w, 200, map[string]any{
		"points":     res.Points,
		"boxes":      boxes,
		"cell_w":     res.CellW,
		"violations": layout.GridViolations(res.Points),
		"grid":       true,
		"loose":      loose,
	})
}
