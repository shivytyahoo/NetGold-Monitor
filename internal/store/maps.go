package store

import (
	"time"

	"netgold/internal/devicon"
)

// Device monitors + imported maps (v1.1). Maps come from the legacy system's
// MapView/Annotation tables; objects keep their imported positions.

// AddDeviceMonitor stores a monitor check; the returned bool reports
// whether a new row was actually inserted (false = identical check
// already existed, e.g. two legacy devices merged into one NetGold device).
func (s *Store) AddDeviceMonitor(m DeviceMonitor) (bool, error) {
	res, err := s.DB.Exec(`INSERT INTO device_monitors(device_id, kind, name, port, interval_sec, timeout_sec, retries, target) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(device_id, kind, name, port) DO NOTHING`,
		m.DeviceID, m.Kind, m.Name, m.Port, m.IntervalSec, m.TimeoutSec, m.Retries, m.Target)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteDeviceMonitor removes one monitor assignment from a device.
func (s *Store) DeleteDeviceMonitor(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM device_monitors WHERE id=?`, id)
	return err
}

const monitorCols = `id, device_id, kind, name, port, interval_sec, timeout_sec, retries, target, state, last_check, last_detail`

func scanMonitor(scan func(...any) error) (DeviceMonitor, error) {
	var m DeviceMonitor
	err := scan(&m.ID, &m.DeviceID, &m.Kind, &m.Name, &m.Port, &m.IntervalSec, &m.TimeoutSec,
		&m.Retries, &m.Target, &m.State, &m.LastCheck, &m.LastDetail)
	return m, err
}

// TransitionMonitorState records one executed service-check result: it
// updates the monitor's state/last-check/detail and — only on an actual
// state change from a previously known state — inserts one event with
// kind "monitor" into the device feed. It reports whether a transition
// event was written. Device status is NOT affected: it remains driven by
// ping/SNMP exactly as before.
func (s *Store) TransitionMonitorState(m DeviceMonitor, newState, detail string) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var prev string
	if err := tx.QueryRow(`SELECT state FROM device_monitors WHERE id=?`, m.ID).Scan(&prev); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE device_monitors SET state=?, last_check=?, last_detail=? WHERE id=?`,
		newState, time.Now().Unix(), detail, m.ID); err != nil {
		return false, err
	}
	transitioned := prev != "" && prev != newState
	if transitioned {
		msg := detail
		if _, err := tx.Exec(`INSERT INTO events(device_id, ts, kind, message) VALUES(?,?,?,?)`,
			m.DeviceID, time.Now().Unix(), "monitor", msg); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return transitioned, nil
}

// UpdateDeviceFlags overwrites the import-related flags of a device
// (used by the importer when merging duplicate-IP devices).
func (s *Store) UpdateDeviceFlags(id int64, needsCredentials, snmpDisabled bool) error {
	b2i := func(b bool) int64 {
		if b {
			return 1
		}
		return 0
	}
	_, err := s.DB.Exec(`UPDATE devices SET needs_credentials=?, snmp_disabled=? WHERE id=?`,
		b2i(needsCredentials), b2i(snmpDisabled), id)
	return err
}

func (s *Store) ListDeviceMonitors(deviceID int64) ([]DeviceMonitor, error) {
	rows, err := s.DB.Query(`SELECT `+monitorCols+` FROM device_monitors WHERE device_id=? ORDER BY kind, name, port`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceMonitor
	for rows.Next() {
		if m, err := scanMonitor(rows.Scan); err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// ListServiceMonitors returns every executable service monitor across all
// devices (the poller's work list for service checks).
func (s *Store) ListServiceMonitors() ([]DeviceMonitor, error) {
	rows, err := s.DB.Query(`SELECT ` + monitorCols + ` FROM device_monitors WHERE kind='service' ORDER BY device_id, name, port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceMonitor
	for rows.Next() {
		if m, err := scanMonitor(rows.Scan); err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *Store) CountDeviceMonitors() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM device_monitors`).Scan(&n)
	return n, err
}

// UpsertMap inserts or updates a map (keyed by its the legacy system MapView id)
// and returns our id.
func (s *Store) UpsertMap(m Map) (int64, error) {
	_, err := s.DB.Exec(`INSERT INTO maps(source_map_id, name, group_id, source_group_id, min_x, min_y, max_x, max_y, props)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_map_id) DO UPDATE SET name=excluded.name, group_id=excluded.group_id,
			source_group_id=excluded.source_group_id, min_x=excluded.min_x, min_y=excluded.min_y,
			max_x=excluded.max_x, max_y=excluded.max_y, props=excluded.props`,
		m.SourceMapID, m.Name, m.GroupID, m.SourceGroupID, m.MinX, m.MinY, m.MaxX, m.MaxY, m.Props)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRow(`SELECT id FROM maps WHERE source_map_id=?`, m.SourceMapID).Scan(&id)
	return id, err
}

// ReplaceMapObjects swaps all objects of a map in one transaction, so a
// re-import is idempotent.
func (s *Store) ReplaceMapObjects(mapID int64, objs []MapObject) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM map_objects WHERE map_id=?`, mapID); err != nil {
		return err
	}
	for _, o := range objs {
		if _, err := tx.Exec(`INSERT INTO map_objects(map_id, source_annotation_id, type, device_id, source_device_id,
			group_id, source_group_id, x, y, x2, y2, w, h, zorder, text, extra)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			mapID, o.SourceAnnotationID, o.Type, o.DeviceID, o.SourceDeviceID, o.GroupID, o.SourceGroupID,
			o.X, o.Y, o.X2, o.Y2, o.W, o.H, o.ZOrder, o.Text, o.Extra); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListMaps() ([]Map, error) {
	rows, err := s.DB.Query(`SELECT m.id, m.source_map_id, m.name, m.group_id, m.source_group_id,
		m.min_x, m.min_y, m.max_x, m.max_y, COUNT(o.id),
		COALESCE(SUM(CASE WHEN o.type='Device' THEN 1 ELSE 0 END), 0)
		FROM maps m LEFT JOIN map_objects o ON o.map_id=m.id
		GROUP BY m.id ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Map
	for rows.Next() {
		var m Map
		if err := rows.Scan(&m.ID, &m.SourceMapID, &m.Name, &m.GroupID, &m.SourceGroupID,
			&m.MinX, &m.MinY, &m.MaxX, &m.MaxY, &m.Objects, &m.Devices); err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetMap(id int64) (*Map, error) {
	var m Map
	err := s.DB.QueryRow(`SELECT id, source_map_id, name, group_id, source_group_id, min_x, min_y, max_x, max_y, IFNULL(props,'')
		FROM maps WHERE id=?`, id).
		Scan(&m.ID, &m.SourceMapID, &m.Name, &m.GroupID, &m.SourceGroupID, &m.MinX, &m.MinY, &m.MaxX, &m.MaxY, &m.Props)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) ListMapObjects(mapID int64) ([]MapObject, error) {
	rows, err := s.DB.Query(`SELECT o.id, o.map_id, o.source_annotation_id, o.type, o.device_id, o.source_device_id,
		o.group_id, o.source_group_id, o.x, o.y, o.x2, o.y2, o.w, o.h, o.zorder, IFNULL(o.text,''), IFNULL(o.extra,''),
		IFNULL(d.name,''), IFNULL(d.ip,''), IFNULL(d.status,''), IFNULL(g.name,''),
		IFNULL(d.device_type,''), IFNULL(d.type_icon,''), IFNULL(d.type,''),
		(SELECT COALESCE(MAX(e.ts),0) FROM events e WHERE e.device_id=d.id AND e.kind IN ('up','down'))
		FROM map_objects o
		LEFT JOIN devices d ON d.id=o.device_id
		LEFT JOIN groups_ g ON g.id=o.group_id
		WHERE o.map_id=? ORDER BY o.zorder, o.id`, mapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapObject
	for rows.Next() {
		var o MapObject
		var pollerType string
		if err := rows.Scan(&o.ID, &o.MapID, &o.SourceAnnotationID, &o.Type, &o.DeviceID, &o.SourceDeviceID,
			&o.GroupID, &o.SourceGroupID, &o.X, &o.Y, &o.X2, &o.Y2, &o.W, &o.H, &o.ZOrder, &o.Text, &o.Extra,
			&o.DeviceName, &o.DeviceIP, &o.DeviceStatus, &o.GroupName,
			&o.DeviceType, &o.TypeIcon, &pollerType, &o.LastChange); err == nil {
			if o.Type == "Device" {
				ic := devicon.ForDevice(o.DeviceType, o.TypeIcon, pollerType)
				o.IconID, o.IconAccent = ic.ID, ic.Accent
			}
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

func (s *Store) CountMapObjects() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM map_objects`).Scan(&n)
	return n, err
}
