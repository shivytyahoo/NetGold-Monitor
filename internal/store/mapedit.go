package store

import (
	"database/sql"
	"errors"
	"math"
)

// Map editing (v3.7): the Maps page gains an administrator-only Edit
// Mode. Imported map objects keep the source annotation ids they
// arrived with; objects a user creates in Edit Mode need ids in the
// same UNIQUE(map_id, source_annotation_id) space that can never
// collide with an imported one, so user-created objects take
// negative source ids (-1, -2, ... below the map's current minimum).
// Nothing here ever rewrites an imported object except the two
// changes the user explicitly makes in Edit Mode: moving a device
// marker (its attached line endpoints follow, see MoveMapObject)
// and deleting an object the user selected.

var (
	// ErrMapNotFound reports a map id that does not exist.
	ErrMapNotFound = errors.New("map not found")
	// ErrMapObjectNotFound reports a map-object id that does not
	// exist on the given map.
	ErrMapObjectNotFound = errors.New("map object not found")
	// ErrDeviceNotFound reports a device id that does not exist.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDeviceAlreadyOnMap reports an attempt to place a device on
	// a map that already carries a marker for it.
	ErrDeviceAlreadyOnMap = errors.New("device is already on this map")
)

// finite reports whether all values are usable map coordinates.
func finite(vals ...float64) bool {
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// nextUserAnnotationID returns the next free negative source
// annotation id for user-created objects on one map.
func nextUserAnnotationID(tx *sql.Tx, mapID int64) (int64, error) {
	var min sql.NullInt64
	if err := tx.QueryRow(`SELECT MIN(source_annotation_id) FROM map_objects WHERE map_id=?`, mapID).Scan(&min); err != nil {
		return 0, err
	}
	if min.Valid && min.Int64 < 0 {
		return min.Int64 - 1, nil
	}
	return -1, nil
}

// mapExtentsTx reads a map's stored extents inside a transaction;
// the boolean reports whether the map exists.
func mapExtentsTx(tx *sql.Tx, mapID int64) (minX, minY, maxX, maxY float64, ok bool, err error) {
	err = tx.QueryRow(`SELECT min_x, min_y, max_x, max_y FROM maps WHERE id=?`, mapID).
		Scan(&minX, &minY, &maxX, &maxY)
	if err == sql.ErrNoRows {
		return 0, 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, 0, false, err
	}
	return minX, minY, maxX, maxY, true, nil
}

// expandMapExtentsTx grows a map's stored extents so the point
// (x, y) sits inside them with a small margin, so a device placed
// or moved outside the imported drawing stays visible in the
// faithful fit view afterwards. Extents only ever grow here.
func expandMapExtentsTx(tx *sql.Tx, mapID int64, x, y float64) error {
	minX, minY, maxX, maxY, ok, err := mapExtentsTx(tx, mapID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrMapNotFound
	}
	const margin = 60.0
	grown := false
	if x-margin < minX {
		minX = x - margin
		grown = true
	}
	if x+margin > maxX {
		maxX = x + margin
		grown = true
	}
	if y-margin < minY {
		minY = y - margin
		grown = true
	}
	if y+margin > maxY {
		maxY = y + margin
		grown = true
	}
	if !grown {
		return nil
	}
	_, err = tx.Exec(`UPDATE maps SET min_x=?, min_y=?, max_x=?, max_y=? WHERE id=?`,
		minX, minY, maxX, maxY, mapID)
	return err
}

// GetMapObject returns one raw map object of a map (no read-time
// joins), or ErrMapObjectNotFound.
func (s *Store) GetMapObject(mapID, objectID int64) (*MapObject, error) {
	var o MapObject
	err := s.DB.QueryRow(`SELECT id, map_id, source_annotation_id, type, device_id, source_device_id,
		group_id, source_group_id, x, y, x2, y2, w, h, zorder, IFNULL(text,''), IFNULL(extra,'')
		FROM map_objects WHERE map_id=? AND id=?`, mapID, objectID).
		Scan(&o.ID, &o.MapID, &o.SourceAnnotationID, &o.Type, &o.DeviceID, &o.SourceDeviceID,
			&o.GroupID, &o.SourceGroupID, &o.X, &o.Y, &o.X2, &o.Y2, &o.W, &o.H, &o.ZOrder, &o.Text, &o.Extra)
	if err == sql.ErrNoRows {
		return nil, ErrMapObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// AddMapObject places a new user-created object on a map: a Device
// marker (o.Type "Device" with o.DeviceID set) or a Line
// (o.Type "Line" with both endpoints set). The object's source
// annotation id is assigned here (the next negative id, see the
// package comment above), and the map's stored extents grow to
// contain the new object. Placing a device that already has a
// marker on this map is refused with ErrDeviceAlreadyOnMap.
func (s *Store) AddMapObject(o MapObject) (MapObject, error) {
	if o.Type != "Device" && o.Type != "Line" {
		return MapObject{}, errors.New("only Device and Line objects can be added to a map")
	}
	if !finite(o.X, o.Y, o.X2, o.Y2) {
		return MapObject{}, errors.New("map coordinates must be finite numbers")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return MapObject{}, err
	}
	defer tx.Rollback()
	if _, _, _, _, ok, err := mapExtentsTx(tx, o.MapID); err != nil {
		return MapObject{}, err
	} else if !ok {
		return MapObject{}, ErrMapNotFound
	}
	if o.Type == "Device" {
		if o.DeviceID == nil || *o.DeviceID == 0 {
			return MapObject{}, errors.New("a device marker needs a device")
		}
		var devName string
		if err := tx.QueryRow(`SELECT name FROM devices WHERE id=?`, *o.DeviceID).Scan(&devName); err != nil {
			if err == sql.ErrNoRows {
				return MapObject{}, ErrDeviceNotFound
			}
			return MapObject{}, err
		}
		var dup int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM map_objects WHERE map_id=? AND type='Device' AND device_id=?`,
			o.MapID, *o.DeviceID).Scan(&dup); err != nil {
			return MapObject{}, err
		}
		if dup > 0 {
			return MapObject{}, ErrDeviceAlreadyOnMap
		}
		if o.W <= 0 {
			o.W = 75 // the imported icon size, so the marker renders like an imported one
		}
		if o.H <= 0 {
			o.H = o.W
		}
	}
	annID, err := nextUserAnnotationID(tx, o.MapID)
	if err != nil {
		return MapObject{}, err
	}
	o.SourceAnnotationID = annID
	if o.ZOrder == 0 {
		var maxZ sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(zorder) FROM map_objects WHERE map_id=?`, o.MapID).Scan(&maxZ); err != nil {
			return MapObject{}, err
		}
		if maxZ.Valid {
			o.ZOrder = int(maxZ.Int64) + 1
		}
	}
	res, err := tx.Exec(`INSERT INTO map_objects(map_id, source_annotation_id, type, device_id, source_device_id,
		group_id, source_group_id, x, y, x2, y2, w, h, zorder, text, extra)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.MapID, o.SourceAnnotationID, o.Type, o.DeviceID, o.SourceDeviceID, o.GroupID, o.SourceGroupID,
		o.X, o.Y, o.X2, o.Y2, o.W, o.H, o.ZOrder, o.Text, o.Extra)
	if err != nil {
		return MapObject{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MapObject{}, err
	}
	o.ID = id
	// The whole object must stay inside the stored extents: both
	// endpoints of a line, the centre of a device marker.
	if err := expandMapExtentsTx(tx, o.MapID, o.X, o.Y); err != nil {
		return MapObject{}, err
	}
	if o.Type == "Line" {
		if err := expandMapExtentsTx(tx, o.MapID, o.X2, o.Y2); err != nil {
			return MapObject{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MapObject{}, err
	}
	return o, nil
}

// MoveMapObject moves one Device marker of a map to a new stored
// position. Lines belong to markers geometrically (see
// ResolveMapLines): the stored endpoints of every line that
// resolved to the moved marker are rewritten to the marker's new
// centre in the same transaction, so the line stays attached after
// the move however far the marker travels. The map's stored
// extents grow to contain the new position.
func (s *Store) MoveMapObject(mapID, objectID int64, x, y float64) error {
	if !finite(x, y) {
		return errors.New("map coordinates must be finite numbers")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	obj, err := getMapObjectTx(tx, mapID, objectID)
	if err != nil {
		return err
	}
	if obj.Type != "Device" {
		return errors.New("only a device marker can be moved")
	}
	// Resolve the map's lines against the CURRENT positions, then
	// move every endpoint that belongs to this marker along with it.
	objs, err := listMapObjectsTx(tx, mapID)
	if err != nil {
		return err
	}
	resolved := ResolveMapLines(objs)
	for lineID, r := range resolved {
		if r.FromObject == objectID {
			if _, err := tx.Exec(`UPDATE map_objects SET x=?, y=? WHERE map_id=? AND id=?`,
				x, y, mapID, lineID); err != nil {
				return err
			}
		}
		if r.ToObject == objectID {
			if _, err := tx.Exec(`UPDATE map_objects SET x2=?, y2=? WHERE map_id=? AND id=?`,
				x, y, mapID, lineID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE map_objects SET x=?, y=? WHERE map_id=? AND id=?`,
		x, y, mapID, objectID); err != nil {
		return err
	}
	if err := expandMapExtentsTx(tx, mapID, x, y); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteMapObject removes exactly one object from one map — a
// device marker or a line. The device itself (and its markers on
// other maps) is never touched by removing its marker here.
func (s *Store) DeleteMapObject(mapID, objectID int64) error {
	res, err := s.DB.Exec(`DELETE FROM map_objects WHERE map_id=? AND id=?`, mapID, objectID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrMapObjectNotFound
	}
	return nil
}

func getMapObjectTx(tx *sql.Tx, mapID, objectID int64) (*MapObject, error) {
	var o MapObject
	err := tx.QueryRow(`SELECT id, map_id, source_annotation_id, type, device_id, source_device_id,
		group_id, source_group_id, x, y, x2, y2, w, h, zorder, IFNULL(text,''), IFNULL(extra,'')
		FROM map_objects WHERE map_id=? AND id=?`, mapID, objectID).
		Scan(&o.ID, &o.MapID, &o.SourceAnnotationID, &o.Type, &o.DeviceID, &o.SourceDeviceID,
			&o.GroupID, &o.SourceGroupID, &o.X, &o.Y, &o.X2, &o.Y2, &o.W, &o.H, &o.ZOrder, &o.Text, &o.Extra)
	if err == sql.ErrNoRows {
		return nil, ErrMapObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// listMapObjectsTx lists a map's raw objects inside a transaction
// (the same rows ListMapObjects joins read-time fields onto).
func listMapObjectsTx(tx *sql.Tx, mapID int64) ([]MapObject, error) {
	rows, err := tx.Query(`SELECT id, map_id, source_annotation_id, type, device_id, source_device_id,
		group_id, source_group_id, x, y, x2, y2, w, h, zorder, IFNULL(text,''), IFNULL(extra,'')
		FROM map_objects WHERE map_id=? ORDER BY zorder, id`, mapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapObject
	for rows.Next() {
		var o MapObject
		if err := rows.Scan(&o.ID, &o.MapID, &o.SourceAnnotationID, &o.Type, &o.DeviceID, &o.SourceDeviceID,
			&o.GroupID, &o.SourceGroupID, &o.X, &o.Y, &o.X2, &o.Y2, &o.W, &o.H, &o.ZOrder, &o.Text, &o.Extra); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
