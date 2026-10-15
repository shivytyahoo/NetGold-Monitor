package web

// Map editing endpoints (v3.7). The Maps page's Edit Mode lets an
// administrator change a map: place a device on it, move a device
// marker (its attached lines follow), remove a marker, and add or
// delete link lines. Every endpoint here is a mutation, so the
// central auth guard already refuses viewer sessions with 403 —
// these handlers only ever run for an administrator.

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"netgold/internal/store"
)

// mapObjectRequest is the body of POST /api/maps/{id}/objects.
// For a device marker: type "Device" with device_id, placed at
// x/y (the centre of the map's stored extents when omitted).
// For a link: type "Line" with from_device_id and to_device_id —
// the line's endpoints are the two devices' marker centres on
// this map, so the line resolves to exactly those devices.
type mapObjectRequest struct {
	Type         string   `json:"type"`
	DeviceID     *int64   `json:"device_id"`
	FromDeviceID *int64   `json:"from_device_id"`
	ToDeviceID   *int64   `json:"to_device_id"`
	X            *float64 `json:"x"`
	Y            *float64 `json:"y"`
}

func finiteCoord(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s *Server) mapObjectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMapNotFound):
		errJSON(w, http.StatusNotFound, "map not found")
	case errors.Is(err, store.ErrMapObjectNotFound):
		errJSON(w, http.StatusNotFound, "map object not found")
	case errors.Is(err, store.ErrDeviceNotFound):
		errJSON(w, http.StatusNotFound, "device not found")
	case errors.Is(err, store.ErrDeviceAlreadyOnMap):
		errJSON(w, http.StatusConflict, "That device is already on this map.")
	default:
		errJSON(w, http.StatusBadRequest, err.Error())
	}
}

// deviceMarkerOnMap finds the Device object of one device on one
// map, or nil when the device has no marker there.
func (s *Server) deviceMarkerOnMap(mapID, deviceID int64) (*store.MapObject, error) {
	objs, err := s.Store.ListMapObjects(mapID)
	if err != nil {
		return nil, err
	}
	for i := range objs {
		if objs[i].Type == "Device" && objs[i].DeviceID != nil && *objs[i].DeviceID == deviceID {
			return &objs[i], nil
		}
	}
	return nil, nil
}

func (s *Server) addMapObject(w http.ResponseWriter, r *http.Request) {
	mapID := pathID(r)
	m, err := s.Store.GetMap(mapID)
	if err != nil {
		errJSON(w, http.StatusNotFound, "map not found")
		return
	}
	var req mapObjectRequest
	if !decode(w, r, &req) {
		return
	}
	switch req.Type {
	case "Device":
		if req.DeviceID == nil || *req.DeviceID == 0 {
			errJSON(w, http.StatusBadRequest, "device_id is required")
			return
		}
		// Default placement: the centre of the map's stored
		// extents; the marker is draggable from there.
		x := (m.MinX + m.MaxX) / 2
		y := (m.MinY + m.MaxY) / 2
		if req.X != nil {
			x = *req.X
		}
		if req.Y != nil {
			y = *req.Y
		}
		if !finiteCoord(x) || !finiteCoord(y) {
			errJSON(w, http.StatusBadRequest, "coordinates must be finite numbers")
			return
		}
		obj, err := s.Store.AddMapObject(store.MapObject{
			MapID: mapID, Type: "Device", DeviceID: req.DeviceID, X: x, Y: y,
		})
		if err != nil {
			s.mapObjectError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, obj)
	case "Line":
		if req.FromDeviceID == nil || req.ToDeviceID == nil {
			errJSON(w, http.StatusBadRequest, "from_device_id and to_device_id are required")
			return
		}
		if *req.FromDeviceID == *req.ToDeviceID {
			errJSON(w, http.StatusBadRequest, "A link needs two different devices.")
			return
		}
		from, err := s.deviceMarkerOnMap(mapID, *req.FromDeviceID)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		to, err := s.deviceMarkerOnMap(mapID, *req.ToDeviceID)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		if from == nil || to == nil {
			errJSON(w, http.StatusBadRequest, "Both devices must be on this map before they can be linked.")
			return
		}
		obj, err := s.Store.AddMapObject(store.MapObject{
			MapID: mapID, Type: "Line", X: from.X, Y: from.Y, X2: to.X, Y2: to.Y,
		})
		if err != nil {
			s.mapObjectError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, obj)
	default:
		errJSON(w, http.StatusBadRequest, "type must be Device or Line")
	}
}

func (s *Server) moveMapObject(w http.ResponseWriter, r *http.Request) {
	mapID := pathID(r)
	oid, _ := strconv.ParseInt(r.PathValue("oid"), 10, 64)
	var req struct {
		X *float64 `json:"x"`
		Y *float64 `json:"y"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.X == nil || req.Y == nil {
		errJSON(w, http.StatusBadRequest, "x and y are required")
		return
	}
	if !finiteCoord(*req.X) || !finiteCoord(*req.Y) {
		errJSON(w, http.StatusBadRequest, "coordinates must be finite numbers")
		return
	}
	if err := s.Store.MoveMapObject(mapID, oid, *req.X, *req.Y); err != nil {
		s.mapObjectError(w, err)
		return
	}
	obj, err := s.Store.GetMapObject(mapID, oid)
	if err != nil {
		s.mapObjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (s *Server) deleteMapObject(w http.ResponseWriter, r *http.Request) {
	mapID := pathID(r)
	oid, _ := strconv.ParseInt(r.PathValue("oid"), 10, 64)
	if err := s.Store.DeleteMapObject(mapID, oid); err != nil {
		s.mapObjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
