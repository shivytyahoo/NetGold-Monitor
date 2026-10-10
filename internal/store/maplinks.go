package store

import "math"

// Line endpoint resolution (v3.1).
//
// the legacy system stores map lines as pure geometry: exact endpoints, but
// no record of which devices a line connects (its Connectivity table
// is empty in real databases). The endpoints were drawn at — or at the
// edge of — device icons, so NetGold reconstructs the association
// geometrically: an endpoint belongs to the nearest device whose centre
// lies within DefaultLinkRadius map units. Device icons are ~75 units
// across, so an endpoint drawn anywhere on an icon is at most ~55
// units from its centre; 75 leaves headroom without reaching across a
// typical gap to a neighbouring device. The association is labelled
// "reconstructed from the map drawing" wherever it is surfaced.

// DefaultLinkRadius is the maximum centre distance (in map units) at
// which a line endpoint is attributed to a device.
const DefaultLinkRadius = 75.0

// MapDevicePoint is one device's position on a map, for resolution.
type MapDevicePoint struct {
	DeviceID int64
	ObjectID int64 // the map object id of the device marker
	X, Y     float64
	W, H     float64 // icon box size (for edge clipping)
	Status   string
}

// nearestPoint returns the index into pts of the device marker whose
// centre is nearest to (x, y), or -1 when no marker lies within
// radius. Ties go to the marker that appears first (callers pass map
// order).
func nearestPoint(x, y float64, pts []MapDevicePoint, radius float64) int {
	best := -1
	bestD := radius
	for i, p := range pts {
		d := math.Hypot(p.X-x, p.Y-y)
		if d <= radius && (best < 0 || d < bestD) {
			best, bestD = i, d
		}
	}
	return best
}

// NearestDevice returns the id of the device whose centre is nearest
// to (x, y), or 0 when no device lies within radius.
func NearestDevice(x, y float64, pts []MapDevicePoint, radius float64) int64 {
	if i := nearestPoint(x, y, pts, radius); i >= 0 {
		return pts[i].DeviceID
	}
	return 0
}

// ResolvedLine is the resolution of one Line map object.
type ResolvedLine struct {
	FromDevice int64 // 0 = endpoint not attributed to a device
	ToDevice   int64
	// The map objects (device markers) the endpoints were attributed
	// to. Two markers can belong to the same stored device (several
	// legacy devices merged on a shared IP keep one marker each), so a
	// line between two markers of one device is still a real line.
	FromObject int64
	ToObject   int64
	// Draw endpoints: the stored endpoints, except that an attributed
	// endpoint is moved to the edge of its device's icon box so the
	// line visually touches the icon instead of crossing it.
	X1, Y1, X2, Y2 float64
	State          string // "up", "down", "unknown"
}

// ResolveMapLines resolves every Line object in a map against the
// map's Device objects. objects must be the full object list of ONE
// map (any order). The result is keyed by the line's object id.
// A line whose endpoints both land on the SAME marker is left
// unattributed (a loop drawn on one icon is decoration, not a link);
// endpoints on different markers of the same stored device still
// count as a resolved line.
func ResolveMapLines(objects []MapObject) map[int64]ResolvedLine {
	var pts []MapDevicePoint
	for _, o := range objects {
		if o.Type != "Device" || o.DeviceID == nil || *o.DeviceID == 0 {
			continue
		}
		pts = append(pts, MapDevicePoint{
			DeviceID: *o.DeviceID, ObjectID: o.ID,
			X: o.X, Y: o.Y, W: o.W, H: o.H, Status: o.DeviceStatus,
		})
	}
	out := map[int64]ResolvedLine{}
	for _, o := range objects {
		if o.Type != "Line" {
			continue
		}
		r := ResolvedLine{X1: o.X, Y1: o.Y, X2: o.X2, Y2: o.Y2, State: "unknown"}
		fi := nearestPoint(o.X, o.Y, pts, DefaultLinkRadius)
		ti := nearestPoint(o.X2, o.Y2, pts, DefaultLinkRadius)
		if fi >= 0 && ti >= 0 && fi == ti {
			fi, ti = -1, -1 // both ends on one marker: a drawn loop
		}
		var fromPt, toPt *MapDevicePoint
		if fi >= 0 {
			fromPt = &pts[fi]
			r.FromDevice, r.FromObject = fromPt.DeviceID, fromPt.ObjectID
			r.X1, r.Y1 = clipToIconEdge(*fromPt, o.X2, o.Y2)
		}
		if ti >= 0 {
			toPt = &pts[ti]
			r.ToDevice, r.ToObject = toPt.DeviceID, toPt.ObjectID
			r.X2, r.Y2 = clipToIconEdge(*toPt, o.X, o.Y)
		}
		r.State = lineState(fromPt, toPt)
		out[o.ID] = r
	}
	return out
}

// clipToIconEdge returns the point where the segment from the device
// centre towards (tx, ty) leaves the device's icon — so a resolved
// line is drawn icon-edge to icon-edge. The edge distance matches the
// size the web UI actually draws the icon at (the stored dimension
// clamped to 46–78 units), so the line ends exactly at the visible
// icon edge whatever dimension the legacy system stored.
func clipToIconEdge(p MapDevicePoint, tx, ty float64) (float64, float64) {
	size := math.Max(p.W, p.H)
	if size < 46 {
		size = 46
	}
	if size > 78 {
		size = 78
	}
	half := size / 2
	dx, dy := tx-p.X, ty-p.Y
	d := math.Hypot(dx, dy)
	if d == 0 {
		return p.X, p.Y
	}
	return p.X + dx/d*half, p.Y + dy/d*half
}

// lineState folds the endpoint markers' device statuses into one
// line state: any attributed end down makes the line down; all
// attributed ends up makes it up; anything else (including no
// attributed ends) is unknown.
func lineState(from, to *MapDevicePoint) string {
	seen := 0
	allUp := true
	for _, p := range []*MapDevicePoint{from, to} {
		if p == nil {
			continue
		}
		seen++
		switch p.Status {
		case "down":
			return "down"
		case "up":
		default:
			allUp = false
		}
	}
	if seen > 0 && allUp {
		return "up"
	}
	return "unknown"
}
