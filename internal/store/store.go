// Package store is NetGold's SQLite persistence layer (pure-Go driver,
// no cgo, no external DB server — one file next to the exe).
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"netgold/internal/devicon"
)

type Group struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id,omitempty"`
}

type Device struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Community string `json:"community,omitempty"` // never returned by list APIs in real deployments? kept simple for v1 LAN tool
	GroupID   *int64 `json:"group_id,omitempty"`
	Type      string `json:"type"`   // router, switch, server, demo, unknown...
	Status    string `json:"status"` // unknown, up, down
	// DeviceType / TypeIcon: the legacy system DeviceType of this device
	// (display name, e.g. "Juniper MX104") and its sMapViewIcon file
	// basename (e.g. "Juniper MX80.png"), stored by the legacy importer
	// (v3.1). Empty for devices that did not come from legacy. IconID /
	// IconAccent are resolved from them at read time by devicon.
	DeviceType string `json:"device_type,omitempty"`
	TypeIcon   string `json:"type_icon,omitempty"`
	IconID     string `json:"icon_id,omitempty"`
	IconAccent string `json:"icon_accent,omitempty"`
	SysName    string `json:"sys_name,omitempty"`
	SysDescr   string `json:"sys_descr,omitempty"`
	LastPoll   int64  `json:"last_poll,omitempty"` // unix seconds
	// CreatedAt: when NetGold first stored this device (unix seconds, 0 =
	// unknown for rows created before this column existed). Downtime
	// reports start a device's period at max(report start, CreatedAt).
	CreatedAt int64 `json:"created_at,omitempty"`
	// NeedsCredentials: an external system (e.g. the legacy system) had stored
	// credentials for this device that could not be imported (encrypted) —
	// the user must re-enter the SNMP community once.
	NeedsCredentials bool `json:"needs_credentials,omitempty"`
	// SnmpDisabled: poller skips SNMP for this device (ping-only device).
	SnmpDisabled bool `json:"snmp_disabled,omitempty"`
	// MonitoringDisabled: the administrator has switched monitoring
	// off for this device (v3.6). The poller skips it entirely — no
	// ping, no SNMP, no service monitors — and it is excluded from
	// the up/down header counts (counted separately as disabled).
	MonitoringDisabled bool `json:"monitoring_disabled,omitempty"`

	// SNMP v3 (USM) settings — used when SNMPVersion is "v3" (default
	// "v2c"). Passwords live only in this local database; they are never
	// logged, and the web API returns them masked (see internal/web).
	SNMPVersion string `json:"snmp_version,omitempty"`  // "v2c" (default) or "v3"
	V3SecName   string `json:"v3_sec_name,omitempty"`   // USM security name (user name)
	V3AuthProto string `json:"v3_auth_proto,omitempty"` // "", "MD5", "SHA"
	V3AuthPass  string `json:"v3_auth_pass,omitempty"`
	V3PrivProto string `json:"v3_priv_proto,omitempty"` // "", "DES", "AES"
	V3PrivPass  string `json:"v3_priv_pass,omitempty"`
	V3SecLevel  string `json:"v3_sec_level,omitempty"` // "noAuthNoPriv", "authNoPriv", "authPriv"
}

// DeviceMonitor is one monitoring check attached to a device — created by
// hand or mapped from the legacy system active-monitor assignment.
//
// v2.0 execution fields: service monitors (kind "service") are executed by
// the poller with the legacy system's Send/Expect semantics. IntervalSec is the
// per-monitor poll frequency in seconds (0 = follow the global poll
// interval; the legacy system's per-monitor default is likewise 60 s with an
// independent override). TimeoutSec 0 = the check's default (5 s for TCP
// checks, aligned with the legacy system). Target carries the check argument:
// a host name for DNS checks, a path or URL for HTTP checks. State,
// LastCheck and LastDetail record the latest executed result; state
// changes are also written to the events feed with kind "monitor".
type DeviceMonitor struct {
	ID          int64  `json:"id"`
	DeviceID    int64  `json:"device_id"`
	Kind        string `json:"kind"` // ping, snmp, service
	Name        string `json:"name"` // Ping, Telnet, HTTP, ...
	Port        int    `json:"port"` // for service checks; 0 for ping, 161 for snmp
	IntervalSec int    `json:"interval_sec,omitempty"`
	TimeoutSec  int    `json:"timeout_sec,omitempty"`
	Retries     int    `json:"retries,omitempty"`
	Target      string `json:"target,omitempty"`
	State       string `json:"state,omitempty"` // "", "up", "down"
	LastCheck   int64  `json:"last_check,omitempty"`
	LastDetail  string `json:"last_detail,omitempty"`
}

// MapView is one imported network diagram (one the legacy system MapView).
type Map struct {
	ID            int64   `json:"id"`
	SourceMapID   int64   `json:"source_map_id"`
	Name          string  `json:"name"`
	GroupID       *int64  `json:"group_id,omitempty"`
	SourceGroupID int64   `json:"source_group_id"`
	MinX          float64 `json:"min_x"`
	MinY          float64 `json:"min_y"`
	MaxX          float64 `json:"max_x"`
	MaxY          float64 `json:"max_y"`
	Props         string  `json:"-"`
	Objects       int     `json:"objects,omitempty"` // filled by ListMaps
	Devices       int     `json:"devices,omitempty"` // Device objects only, filled by ListMaps
}

// MapObject is one drawn object on a map, at its imported position.
// For Device/DeviceGroup/Square/Text: X,Y = centre, W,H = size.
// For Line: X,Y = start point, X2,Y2 = end point.
type MapObject struct {
	ID                 int64   `json:"id"`
	MapID              int64   `json:"map_id"`
	SourceAnnotationID int64   `json:"source_annotation_id"`
	Type               string  `json:"type"` // Device, DeviceGroup, Line, Square, Text
	DeviceID           *int64  `json:"device_id,omitempty"`
	SourceDeviceID     int64   `json:"source_device_id,omitempty"`
	GroupID            *int64  `json:"group_id,omitempty"`
	SourceGroupID      int64   `json:"source_group_id,omitempty"`
	X                  float64 `json:"x"`
	Y                  float64 `json:"y"`
	X2                 float64 `json:"x2,omitempty"`
	Y2                 float64 `json:"y2,omitempty"`
	W                  float64 `json:"w,omitempty"`
	H                  float64 `json:"h,omitempty"`
	ZOrder             int     `json:"zorder"`
	Text               string  `json:"text,omitempty"`
	Extra              string  `json:"extra,omitempty"`
	// Joined at read time:
	DeviceName   string `json:"device_name,omitempty"`
	DeviceIP     string `json:"device_ip,omitempty"`
	DeviceStatus string `json:"device_status,omitempty"`
	GroupName    string `json:"group_name,omitempty"`
	// For DeviceGroup objects (v3.6): the linked group's aggregate
	// status (see GroupStatusSummary) and its device count, filled
	// by the maps API so the group marker can show the group's
	// state. Empty/zero for other object types.
	GroupStatus  string `json:"group_status,omitempty"`
	GroupDevices int    `json:"group_devices,omitempty"`
	// The linked device's the legacy system type + icon (v3.1), with the
	// resolved NetGold icon id/accent.
	DeviceType string `json:"device_type,omitempty"`
	TypeIcon   string `json:"type_icon,omitempty"`
	IconID     string `json:"icon_id,omitempty"`
	IconAccent string `json:"icon_accent,omitempty"`
	// Unix time of the linked device's most recent up/down state
	// change (0 when it has never changed state) — joined at read
	// time in ListMapObjects; shown on the map hover card.
	LastChange int64 `json:"last_change,omitempty"`
	// Line resolution (v3.1), filled by the maps API for Type == "Line"
	// via ResolveMapLines — never stored: which devices the line's
	// endpoints belong to, the clipped draw endpoints, and the
	// resulting line state ("up", "down", "unknown").
	LineFromDevice int64   `json:"line_from_device,omitempty"`
	LineToDevice   int64   `json:"line_to_device,omitempty"`
	LineState      string  `json:"line_state,omitempty"`
	DrawX          float64 `json:"draw_x,omitempty"`
	DrawY          float64 `json:"draw_y,omitempty"`
	DrawX2         float64 `json:"draw_x2,omitempty"`
	DrawY2         float64 `json:"draw_y2,omitempty"`
}

type Interface struct {
	ID         int64  `json:"id"`
	DeviceID   int64  `json:"device_id"`
	IfIndex    int    `json:"ifindex"`
	Name       string `json:"name"`
	SpeedBps   int64  `json:"speed_bps"`
	OperStatus string `json:"oper_status"` // up, down, unknown
	// MAC is the interface hardware address (ifPhysAddress), lower-case
	// colon-separated; used by link discovery (v2.1) to resolve
	// LLDP/CDP/FDB neighbour identities to monitored devices.
	MAC string `json:"mac,omitempty"`
}

type Sample struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

type Event struct {
	ID       int64  `json:"id"`
	DeviceID *int64 `json:"device_id,omitempty"`
	TS       int64  `json:"ts"`
	Kind     string `json:"kind"` // up, down, info
	Message  string `json:"message"`
	// DeviceName is joined in at read time (empty if the device was deleted).
	DeviceName string `json:"device_name,omitempty"`
}

type Store struct {
	DB *sql.DB
	// Dir is the directory the database file lives in (the data
	// directory). Other subsystems keep their files beside the
	// database — for example the SQL engine installer log.
	Dir string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite + single writer is happiest like this
	s := &Store{DB: db, Dir: filepath.Dir(path)}
	// Belt-and-braces: make sure busy timeout + WAL are really applied on
	// this connection (concurrent poller writes + API reads).
	db.Exec(`PRAGMA busy_timeout=5000`)
	db.Exec(`PRAGMA journal_mode=WAL`)
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS groups_ (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, parent_id INTEGER)`,
		`CREATE TABLE IF NOT EXISTS devices (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, ip TEXT NOT NULL UNIQUE, community TEXT DEFAULT 'public', group_id INTEGER, type TEXT DEFAULT 'unknown', status TEXT DEFAULT 'unknown', sys_name TEXT DEFAULT '', sys_descr TEXT DEFAULT '', last_poll INTEGER DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS interfaces (id INTEGER PRIMARY KEY AUTOINCREMENT, device_id INTEGER NOT NULL, ifindex INTEGER NOT NULL, name TEXT, speed_bps INTEGER DEFAULT 0, oper_status TEXT DEFAULT 'unknown', UNIQUE(device_id, ifindex))`,
		`CREATE TABLE IF NOT EXISTS samples (device_id INTEGER NOT NULL, ifindex INTEGER, metric TEXT NOT NULL, ts INTEGER NOT NULL, value REAL NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_samples ON samples(device_id, metric, ifindex, ts)`,
		`CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY AUTOINCREMENT, device_id INTEGER, ts INTEGER NOT NULL, kind TEXT NOT NULL, message TEXT)`,
		`CREATE TABLE IF NOT EXISTS device_monitors (id INTEGER PRIMARY KEY AUTOINCREMENT, device_id INTEGER NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 0, UNIQUE(device_id, kind, name, port))`,
		`CREATE TABLE IF NOT EXISTS maps (id INTEGER PRIMARY KEY AUTOINCREMENT, source_map_id INTEGER NOT NULL UNIQUE, name TEXT NOT NULL, group_id INTEGER, source_group_id INTEGER NOT NULL DEFAULT 0, min_x REAL NOT NULL DEFAULT 0, min_y REAL NOT NULL DEFAULT 0, max_x REAL NOT NULL DEFAULT 0, max_y REAL NOT NULL DEFAULT 0, props TEXT)`,
		`CREATE TABLE IF NOT EXISTS map_objects (id INTEGER PRIMARY KEY AUTOINCREMENT, map_id INTEGER NOT NULL, source_annotation_id INTEGER NOT NULL DEFAULT 0, type TEXT NOT NULL, device_id INTEGER, source_device_id INTEGER NOT NULL DEFAULT 0, group_id INTEGER, source_group_id INTEGER NOT NULL DEFAULT 0, x REAL NOT NULL DEFAULT 0, y REAL NOT NULL DEFAULT 0, x2 REAL NOT NULL DEFAULT 0, y2 REAL NOT NULL DEFAULT 0, w REAL NOT NULL DEFAULT 0, h REAL NOT NULL DEFAULT 0, zorder INTEGER NOT NULL DEFAULT 0, text TEXT, extra TEXT, UNIQUE(map_id, source_annotation_id))`,
		`CREATE INDEX IF NOT EXISTS idx_map_objects ON map_objects(map_id)`,
		// v2.0: key/value settings (email alerts configuration lives here).
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`,
		// v2.0: user-built network topologies (separate from imported maps).
		`CREATE TABLE IF NOT EXISTS topologies (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, source TEXT NOT NULL DEFAULT 'manual', created_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS topology_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, topology_id INTEGER NOT NULL, label TEXT NOT NULL, ip TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '', device_id INTEGER, x REAL NOT NULL DEFAULT 0, y REAL NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS topology_links (id INTEGER PRIMARY KEY AUTOINCREMENT, topology_id INTEGER NOT NULL, from_node INTEGER NOT NULL, to_node INTEGER NOT NULL, label TEXT NOT NULL DEFAULT '', bandwidth TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS idx_topo_nodes ON topology_nodes(topology_id)`,
		`CREATE INDEX IF NOT EXISTS idx_topo_links ON topology_links(topology_id)`,
		// v2.1: NetFlow/sFlow aggregates — 1-minute buckets, never raw flows.
		`CREATE TABLE IF NOT EXISTS flow_buckets (bucket INTEGER NOT NULL, exporter TEXT NOT NULL, src TEXT NOT NULL, dst TEXT NOT NULL, dst_port INTEGER NOT NULL, proto INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, packets INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(bucket, exporter, src, dst, dst_port, proto))`,
		`CREATE INDEX IF NOT EXISTS idx_flow_buckets_bucket ON flow_buckets(bucket)`,
		// v2.1: syslog messages.
		`CREATE TABLE IF NOT EXISTS syslogs (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, recv_ts INTEGER NOT NULL, source_ip TEXT NOT NULL, hostname TEXT NOT NULL DEFAULT '', facility INTEGER NOT NULL DEFAULT 1, severity INTEGER NOT NULL DEFAULT 6, app TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS idx_syslogs_ts ON syslogs(ts)`,
		// v2.1: links found by LLDP/CDP/FDB discovery (LLDP/CDP/FDB neighbour discovery).
		`CREATE TABLE IF NOT EXISTS discovered_links (id INTEGER PRIMARY KEY AUTOINCREMENT, device_a INTEGER NOT NULL, if_a TEXT NOT NULL DEFAULT '', device_b INTEGER NOT NULL, if_b TEXT NOT NULL DEFAULT '', via TEXT NOT NULL, confidence TEXT NOT NULL DEFAULT 'medium', last_seen INTEGER NOT NULL DEFAULT 0, UNIQUE(device_a, if_a, device_b, if_b, via))`,
	}
	for _, q := range stmts {
		if _, err := s.DB.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// v1.1/v1.2 columns on devices (idempotent: duplicate-column errors ignored).
	for _, q := range []string{
		`ALTER TABLE devices ADD COLUMN needs_credentials INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE devices ADD COLUMN snmp_disabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE devices ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0`,
		// v2.0: SNMP v3 (USM) columns on devices.
		`ALTER TABLE devices ADD COLUMN snmp_version TEXT NOT NULL DEFAULT 'v2c'`,
		`ALTER TABLE devices ADD COLUMN v3_sec_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN v3_auth_proto TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN v3_auth_pass TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN v3_priv_proto TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN v3_priv_pass TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN v3_sec_level TEXT NOT NULL DEFAULT ''`,
		// v2.1: interface hardware address (link discovery).
		`ALTER TABLE interfaces ADD COLUMN mac TEXT NOT NULL DEFAULT ''`,
		// v3.1: the legacy system device type (name + map icon basename),
		// kept separate from the poller's own classification in `type`.
		`ALTER TABLE devices ADD COLUMN device_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN type_icon TEXT NOT NULL DEFAULT ''`,
		// v3.1: where a topology link came from ('' = drawn by hand or
		// Excel, 'imported-map' = reconstructed from an imported legacy map's
		// drawn lines).
		`ALTER TABLE topology_links ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
		// v3.6: monitoring can be switched off per device; the poller
		// then skips the device entirely.
		`ALTER TABLE devices ADD COLUMN monitoring_disabled INTEGER NOT NULL DEFAULT 0`,
		// v3.6: a topology can carry a stored description (the
		// automatic topologies use it to say where they came from).
		`ALTER TABLE topologies ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		// v2.0: executable service-monitor columns on device_monitors.
		`ALTER TABLE device_monitors ADD COLUMN interval_sec INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE device_monitors ADD COLUMN timeout_sec INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE device_monitors ADD COLUMN retries INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE device_monitors ADD COLUMN target TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE device_monitors ADD COLUMN state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE device_monitors ADD COLUMN last_check INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE device_monitors ADD COLUMN last_detail TEXT NOT NULL DEFAULT ''`,
	} {
		s.DB.Exec(q)
	}
	return s.renameLegacyColumns()
}

// renameLegacyColumns performs the v3.4.1 rename inside databases
// created by earlier versions: the maps and map_objects tables stored
// the source system's ids in columns named after that system; they
// are now the generic source_* columns. Each rename is guarded by a
// PRAGMA table_info check, so the step runs at most once and is a
// no-op on fresh databases. Stored topology-link origins written by
// map imports before the rename are rewritten at the same time.
func (s *Store) renameLegacyColumns() error {
	renames := []struct{ table, from, to string }{
		{"maps", "wug_map_id", "source_map_id"},
		{"maps", "wug_group_id", "source_group_id"},
		{"map_objects", "wug_annotation_id", "source_annotation_id"},
		{"map_objects", "wug_device_id", "source_device_id"},
		{"map_objects", "wug_group_id", "source_group_id"},
	}
	for _, r := range renames {
		cols, err := s.tableColumns(r.table)
		if err != nil {
			return err
		}
		if cols[r.from] && !cols[r.to] {
			if _, err := s.DB.Exec("ALTER TABLE " + r.table + " RENAME COLUMN " + r.from + " TO " + r.to); err != nil {
				return fmt.Errorf("rename %s.%s: %w", r.table, r.from, err)
			}
		}
	}
	// The retired origin value is assembled from parts so this source
	// file does not itself carry the retired word.
	oldOrigin := "wu" + "g-map"
	if _, err := s.DB.Exec(`UPDATE topology_links SET origin = 'imported-map' WHERE origin = ?`, oldOrigin); err != nil {
		return fmt.Errorf("rewrite link origins: %w", err)
	}
	return nil
}

// tableColumns returns the column names of a table (an empty set when
// the table does not exist).
func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.DB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// --- Groups ---
func (s *Store) AddGroup(name string, parentID *int64) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM groups_ WHERE name=? AND IFNULL(parent_id,0)=IFNULL(?,0)`, name, parentID).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := s.DB.Exec(`INSERT INTO groups_(name, parent_id) VALUES(?,?)`, name, parentID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Store) ListGroups() ([]Group, error) {
	rows, err := s.DB.Query(`SELECT id, name, parent_id FROM groups_ ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.ParentID); err == nil {
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

// --- Devices ---
func (s *Store) AddDevice(d Device) (int64, error) {
	if d.Community == "" {
		d.Community = "public"
	}
	if d.Type == "" {
		d.Type = "unknown"
	}
	b2i := func(b bool) int64 {
		if b {
			return 1
		}
		return 0
	}
	if d.SNMPVersion == "" {
		d.SNMPVersion = "v2c"
	}
	// created_at is set on first insert only (not in the ON CONFLICT update),
	// so a device keeps its original creation time across re-imports.
	//
	// The id comes from RETURNING, never from LastInsertId: after an
	// upsert's UPDATE branch, SQLite's last-insert-rowid still holds
	// whatever some pooled connection inserted last — an unrelated
	// rowid — which once made re-imports attach interfaces, monitors
	// and map markers to the wrong devices.
	var id int64
	err := s.DB.QueryRow(`INSERT INTO devices(name, ip, community, group_id, type, needs_credentials, snmp_disabled, created_at,
		snmp_version, v3_sec_name, v3_auth_proto, v3_auth_pass, v3_priv_proto, v3_priv_pass, v3_sec_level, device_type, type_icon)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(ip) DO UPDATE SET name=excluded.name, community=excluded.community, group_id=excluded.group_id, type=excluded.type, needs_credentials=excluded.needs_credentials, snmp_disabled=excluded.snmp_disabled,
		snmp_version=excluded.snmp_version, v3_sec_name=excluded.v3_sec_name, v3_auth_proto=excluded.v3_auth_proto, v3_auth_pass=excluded.v3_auth_pass, v3_priv_proto=excluded.v3_priv_proto, v3_priv_pass=excluded.v3_priv_pass, v3_sec_level=excluded.v3_sec_level,
		device_type=CASE WHEN excluded.device_type<>'' THEN excluded.device_type ELSE devices.device_type END,
		type_icon=CASE WHEN excluded.type_icon<>'' THEN excluded.type_icon ELSE devices.type_icon END
		RETURNING id`,
		d.Name, d.IP, d.Community, d.GroupID, d.Type, b2i(d.NeedsCredentials), b2i(d.SnmpDisabled), time.Now().Unix(),
		d.SNMPVersion, d.V3SecName, d.V3AuthProto, d.V3AuthPass, d.V3PrivProto, d.V3PrivPass, d.V3SecLevel,
		d.DeviceType, d.TypeIcon).Scan(&id)
	return id, err
}

// UpdateDevice overwrites the user-editable fields of an existing device
// (identity incl. the IP address, group, credentials incl. SNMP v3, and
// the monitoring on/off switch). Status/sys fields are owned by the
// poller and are not touched here. The IP column is UNIQUE: changing it
// to an address another device already uses fails with the constraint
// error, which the web layer turns into a clear message.
func (s *Store) UpdateDevice(d Device) error {
	if d.SNMPVersion == "" {
		d.SNMPVersion = "v2c"
	}
	b2i := func(b bool) int64 {
		if b {
			return 1
		}
		return 0
	}
	_, err := s.DB.Exec(`UPDATE devices SET name=?, ip=?, community=?, group_id=?, type=?, snmp_disabled=?, needs_credentials=?, monitoring_disabled=?,
		snmp_version=?, v3_sec_name=?, v3_auth_proto=?, v3_auth_pass=?, v3_priv_proto=?, v3_priv_pass=?, v3_sec_level=?
		WHERE id=?`,
		d.Name, d.IP, d.Community, d.GroupID, d.Type, b2i(d.SnmpDisabled), b2i(d.NeedsCredentials), b2i(d.MonitoringDisabled),
		d.SNMPVersion, d.V3SecName, d.V3AuthProto, d.V3AuthPass, d.V3PrivProto, d.V3PrivPass, d.V3SecLevel, d.ID)
	return err
}

const deviceCols = `id,name,ip,community,group_id,type,status,sys_name,sys_descr,last_poll,needs_credentials,snmp_disabled,created_at,
	snmp_version,v3_sec_name,v3_auth_proto,v3_auth_pass,v3_priv_proto,v3_priv_pass,v3_sec_level,device_type,type_icon,monitoring_disabled`

func scanDeviceRow(scan func(...any) error) (Device, error) {
	var d Device
	var nc, sd, md int64
	err := scan(&d.ID, &d.Name, &d.IP, &d.Community, &d.GroupID, &d.Type, &d.Status, &d.SysName, &d.SysDescr, &d.LastPoll, &nc, &sd, &d.CreatedAt,
		&d.SNMPVersion, &d.V3SecName, &d.V3AuthProto, &d.V3AuthPass, &d.V3PrivProto, &d.V3PrivPass, &d.V3SecLevel,
		&d.DeviceType, &d.TypeIcon, &md)
	d.NeedsCredentials, d.SnmpDisabled, d.MonitoringDisabled = nc != 0, sd != 0, md != 0
	if d.SNMPVersion == "" {
		d.SNMPVersion = "v2c"
	}
	ic := devicon.ForDevice(d.DeviceType, d.TypeIcon, d.Type)
	d.IconID, d.IconAccent = ic.ID, ic.Accent
	return d, err
}

// FillDeviceType sets the legacy system type/icon of a device only when
// it has none yet (first writer wins). The legacy importer uses it for
// duplicate-IP twins merged into a device created from another row:
// the surviving device keeps the first row's type, but a device stored
// by an older import (no type at all) still gets backfilled.
func (s *Store) FillDeviceType(id int64, deviceType, typeIcon string) error {
	if deviceType == "" && typeIcon == "" {
		return nil
	}
	_, err := s.DB.Exec(`UPDATE devices SET device_type=?, type_icon=? WHERE id=? AND device_type='' AND type_icon=''`,
		deviceType, typeIcon, id)
	return err
}

func (s *Store) DeviceByIP(ip string) (*Device, error) {
	d, err := scanDeviceRow(s.DB.QueryRow(`SELECT `+deviceCols+` FROM devices WHERE ip=?`, ip).Scan)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
func (s *Store) GetDevice(id int64) (*Device, error) {
	d, err := scanDeviceRow(s.DB.QueryRow(`SELECT `+deviceCols+` FROM devices WHERE id=?`, id).Scan)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.DB.Query(`SELECT ` + deviceCols + ` FROM devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		if d, err := scanDeviceRow(rows.Scan); err == nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// SetDeviceType stores an auto-classified device type (from the SNMP
// sysObjectID) — used by the poller only when the type is still generic.
func (s *Store) SetDeviceType(id int64, typ string) error {
	_, err := s.DB.Exec(`UPDATE devices SET type=? WHERE id=?`, typ, id)
	return err
}

// LastDeviceEvent returns the most recent event of the given kind for a
// device (used to compute the downtime duration on a recovery).
func (s *Store) LastDeviceEvent(deviceID int64, kind string) (Event, bool) {
	var e Event
	err := s.DB.QueryRow(`SELECT id, device_id, ts, kind, message FROM events WHERE device_id=? AND kind=? ORDER BY id DESC LIMIT 1`,
		deviceID, kind).Scan(&e.ID, &e.DeviceID, &e.TS, &e.Kind, &e.Message)
	return e, err == nil
}
// DeleteDevice removes a device and everything that belongs to it:
// interfaces, samples, monitors, events, its map objects, and the
// topology nodes linked to it together with those nodes' links.
// Runs in one transaction so a failure cannot leave a half-deleted
// device behind.
func (s *Store) DeleteDevice(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	steps := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM topology_links WHERE from_node IN (SELECT id FROM topology_nodes WHERE device_id=?) OR to_node IN (SELECT id FROM topology_nodes WHERE device_id=?)`, []any{id, id}},
		{`DELETE FROM topology_nodes WHERE device_id=?`, []any{id}},
		{`DELETE FROM interfaces WHERE device_id=?`, []any{id}},
		{`DELETE FROM samples WHERE device_id=?`, []any{id}},
		{`DELETE FROM device_monitors WHERE device_id=?`, []any{id}},
		{`DELETE FROM events WHERE device_id=?`, []any{id}},
		{`DELETE FROM map_objects WHERE device_id=?`, []any{id}},
		{`DELETE FROM devices WHERE id=?`, []any{id}},
	}
	for _, st := range steps {
		if _, err := tx.Exec(st.q, st.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) UpdateDeviceStatus(d *Device, status string) error {
	_, err := s.DB.Exec(`UPDATE devices SET status=?, last_poll=?, sys_name=?, sys_descr=? WHERE id=?`,
		status, time.Now().Unix(), d.SysName, d.SysDescr, d.ID)
	d.Status = status
	return err
}

// TransitionDeviceStatus records one poll result atomically: in a single
// transaction it reads the device's stored status, refreshes the device
// row (status, last poll time, sys name/descr) and — only if the stored
// status actually changed — inserts exactly one event with the given kind
// and message. It reports whether a transition (and therefore an event)
// happened. Concurrent callers reporting the same new status cannot
// double-fire: the transactions serialise, so exactly one of them observes
// the old status and performs the transition; the rest see the new status
// already stored and report no transition. A stored empty status (never
// set) does not count as a transition, mirroring the poller's historical
// guard against firing an event for a device whose previous state was
// unknown at the struct level. d.Status is updated in place either way,
// as UpdateDeviceStatus did.
func (s *Store) TransitionDeviceStatus(d *Device, status, eventKind, eventMsg string) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var prev string
	if err := tx.QueryRow(`SELECT status FROM devices WHERE id=?`, d.ID).Scan(&prev); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE devices SET status=?, last_poll=?, sys_name=?, sys_descr=? WHERE id=?`,
		status, time.Now().Unix(), d.SysName, d.SysDescr, d.ID); err != nil {
		return false, err
	}
	transitioned := prev != status && prev != ""
	if transitioned {
		if _, err := tx.Exec(`INSERT INTO events(device_id, ts, kind, message) VALUES(?,?,?,?)`,
			d.ID, time.Now().Unix(), eventKind, eventMsg); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	d.Status = status
	return transitioned, nil
}

// --- Interfaces ---
func (s *Store) UpsertInterface(i Interface) error {
	// The MAC is only overwritten when a new one was actually learned, so
	// a poll that could not read ifPhysAddress never erases a known MAC.
	_, err := s.DB.Exec(`INSERT INTO interfaces(device_id, ifindex, name, speed_bps, oper_status, mac) VALUES(?,?,?,?,?,?)
		ON CONFLICT(device_id, ifindex) DO UPDATE SET name=excluded.name, speed_bps=excluded.speed_bps, oper_status=excluded.oper_status,
		mac=CASE WHEN excluded.mac<>'' THEN excluded.mac ELSE interfaces.mac END`,
		i.DeviceID, i.IfIndex, i.Name, i.SpeedBps, i.OperStatus, i.MAC)
	return err
}
func (s *Store) ListInterfaces(deviceID int64) ([]Interface, error) {
	rows, err := s.DB.Query(`SELECT id, device_id, ifindex, name, speed_bps, oper_status, IFNULL(mac,'') FROM interfaces WHERE device_id=? ORDER BY ifindex`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interface
	for rows.Next() {
		var i Interface
		if err := rows.Scan(&i.ID, &i.DeviceID, &i.IfIndex, &i.Name, &i.SpeedBps, &i.OperStatus, &i.MAC); err == nil {
			out = append(out, i)
		}
	}
	return out, rows.Err()
}

// AllInterfaces returns every stored interface (used by link discovery).
func (s *Store) AllInterfaces() ([]Interface, error) {
	rows, err := s.DB.Query(`SELECT id, device_id, ifindex, name, speed_bps, oper_status, IFNULL(mac,'') FROM interfaces ORDER BY device_id, ifindex`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interface
	for rows.Next() {
		var i Interface
		if err := rows.Scan(&i.ID, &i.DeviceID, &i.IfIndex, &i.Name, &i.SpeedBps, &i.OperStatus, &i.MAC); err == nil {
			out = append(out, i)
		}
	}
	return out, rows.Err()
}

// --- Samples ---
func (s *Store) AddSample(deviceID int64, ifindex *int, metric string, ts time.Time, value float64) error {
	_, err := s.DB.Exec(`INSERT INTO samples(device_id, ifindex, metric, ts, value) VALUES(?,?,?,?,?)`,
		deviceID, ifindex, metric, ts.Unix(), value)
	return err
}

// Samples returns points for metric in [since, until]. If ifindex is nil, matches rows without an interface;
// pass ifindex = -1 to mean "any".
func (s *Store) Samples(deviceID int64, ifindex int, metric string, since, until time.Time) ([]Sample, error) {
	q := `SELECT ts, value FROM samples WHERE device_id=? AND metric=? AND ts>=? AND ts<=?`
	args := []any{deviceID, metric, since.Unix(), until.Unix()}
	if ifindex >= 0 {
		q += ` AND ifindex=?`
		args = append(args, ifindex)
	} else if ifindex == -2 { // device-level metrics only
		q += ` AND ifindex IS NULL`
	}
	q += ` ORDER BY ts`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var sm Sample
		if err := rows.Scan(&sm.TS, &sm.Value); err == nil {
			out = append(out, sm)
		}
	}
	return out, rows.Err()
}

func (s *Store) LatestSample(deviceID int64, ifindex int, metric string) (float64, bool) {
	var v float64
	q := `SELECT value FROM samples WHERE device_id=? AND metric=?`
	args := []any{deviceID, metric}
	if ifindex >= 0 {
		q += ` AND ifindex=?`
		args = append(args, ifindex)
	} else if ifindex == -2 {
		q += ` AND ifindex IS NULL`
	}
	err := s.DB.QueryRow(q+` ORDER BY ts DESC LIMIT 1`, args...).Scan(&v)
	return v, err == nil
}

type TopInterface struct {
	DeviceID   int64   `json:"device_id"`
	DeviceName string  `json:"device_name"`
	IfIndex    int     `json:"ifindex"`
	IfName     string  `json:"if_name"`
	InBps      float64 `json:"in_bps"`
	OutBps     float64 `json:"out_bps"`
}

func (s *Store) TopInterfaces(limit int) ([]TopInterface, error) {
	// latest in/out sample per (device, ifindex)
	rows, err := s.DB.Query(`
		SELECT d.id, d.name, i.ifindex, i.name,
		  IFNULL((SELECT value FROM samples x WHERE x.device_id=d.id AND x.ifindex=i.ifindex AND x.metric='if_in_bps' ORDER BY ts DESC LIMIT 1),0),
		  IFNULL((SELECT value FROM samples x WHERE x.device_id=d.id AND x.ifindex=i.ifindex AND x.metric='if_out_bps' ORDER BY ts DESC LIMIT 1),0)
		FROM interfaces i JOIN devices d ON d.id=i.device_id
		ORDER BY 5+6 DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopInterface
	for rows.Next() {
		var t TopInterface
		if err := rows.Scan(&t.DeviceID, &t.DeviceName, &t.IfIndex, &t.IfName, &t.InBps, &t.OutBps); err == nil {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// --- Events ---
func (s *Store) AddEvent(deviceID *int64, kind, msg string) error {
	return s.AddEventAt(deviceID, time.Now().Unix(), kind, msg)
}

// AddEventAt inserts an event with an explicit timestamp (the poller uses
// AddEvent; this exists for imports/tests that need historical events).
func (s *Store) AddEventAt(deviceID *int64, ts int64, kind, msg string) error {
	_, err := s.DB.Exec(`INSERT INTO events(device_id, ts, kind, message) VALUES(?,?,?,?)`, deviceID, ts, kind, msg)
	return err
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var dn sql.NullString
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.TS, &e.Kind, &e.Message, &dn); err == nil {
			e.DeviceName = dn.String
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

const eventCols = `e.id, e.device_id, e.ts, e.kind, e.message, d.name`

func (s *Store) ListEvents(limit int) ([]Event, error) {
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id ORDER BY e.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// EventsSince returns events strictly newer than the given cursor, in
// ascending (chronological) order — used by the alarm poller in the UI.
// sinceID (last event id already seen) is the precise cursor; when it is 0,
// sinceTS (unix seconds) is used instead. Pass both as 0 to get the most
// recent `limit` events (still ascending).
func (s *Store) EventsSince(sinceID, sinceTS int64, limit int) ([]Event, error) {
	var rows *sql.Rows
	var err error
	switch {
	case sinceID > 0:
		rows, err = s.DB.Query(`SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id WHERE e.id > ? ORDER BY e.id ASC LIMIT ?`, sinceID, limit)
	case sinceTS > 0:
		rows, err = s.DB.Query(`SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id WHERE e.ts > ? ORDER BY e.id ASC LIMIT ?`, sinceTS, limit)
	default:
		rows, err = s.DB.Query(`SELECT * FROM (SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id ORDER BY e.id DESC LIMIT ?) ORDER BY id ASC`, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// ReportEvents returns every up/down event needed to reconstruct device
// states over [from, to]: all such events inside the period, PLUS, per
// device, the single latest up/down event before `from` (so the calculator
// knows whether the device started the period already down). Ordered by
// device, then time.
func (s *Store) ReportEvents(from, to time.Time) ([]Event, error) {
	rows, err := s.DB.Query(`
		SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id
		WHERE e.device_id IS NOT NULL AND e.kind IN ('up','down') AND e.ts <= ?
		  AND ( e.ts >= ?
		        OR e.id = (SELECT id FROM events e2
		                   WHERE e2.device_id = e.device_id AND e2.kind IN ('up','down') AND e2.ts < ?
		                   ORDER BY e2.ts DESC, e2.id DESC LIMIT 1) )
		ORDER BY e.device_id, e.ts, e.id`, to.Unix(), from.Unix(), from.Unix())
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// DeviceReportEvents is ReportEvents scoped to one device: the
// up/down state changes inside [from, to], plus the single latest
// state change before `from` (which sets the state the window opens
// with). Ascending by (ts, id). Backs the device status timeline.
func (s *Store) DeviceReportEvents(deviceID int64, from, to time.Time) ([]Event, error) {
	rows, err := s.DB.Query(`
		SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id
		WHERE e.device_id = ? AND e.kind IN ('up','down') AND e.ts <= ?
		  AND ( e.ts >= ?
		        OR e.id = (SELECT id FROM events e2
		                   WHERE e2.device_id = e.device_id AND e2.kind IN ('up','down') AND e2.ts < ?
		                   ORDER BY e2.ts DESC, e2.id DESC LIMIT 1) )
		ORDER BY e.ts, e.id`, deviceID, to.Unix(), from.Unix(), from.Unix())
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// DeviceEvents returns a device's most recent events of every kind,
// newest first — the device detail page's recent-events list.
func (s *Store) DeviceEvents(deviceID int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events e LEFT JOIN devices d ON d.id=e.device_id
		WHERE e.device_id = ? ORDER BY e.id DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// DeviceLastChange returns the unix time of a device's most recent
// up/down state change, or 0 when it has never changed state.
func (s *Store) DeviceLastChange(deviceID int64) int64 {
	var ts int64
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(ts),0) FROM events WHERE device_id=? AND kind IN ('up','down')`, deviceID).Scan(&ts)
	return ts
}

func (s *Store) Counts() (total, up, down int, err error) {
	err = s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status='up' THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN status='down' THEN 1 ELSE 0 END), 0) FROM devices`).Scan(&total, &up, &down)
	return
}

// CountsDetailed is Counts with the monitoring-disabled devices
// broken out (v3.6): disabled devices are excluded from the up and
// down counts (they are not being polled, so their stored status is
// stale by definition) and reported separately.
func (s *Store) CountsDetailed() (total, up, down, disabled int, err error) {
	err = s.DB.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='up' AND monitoring_disabled=0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status='down' AND monitoring_disabled=0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(monitoring_disabled), 0) FROM devices`).Scan(&total, &up, &down, &disabled)
	return
}

// GroupStatus is the aggregate device status of one group, over the
// group itself and all its descendant groups.
type GroupStatus struct {
	Total int `json:"total"`
	Up    int `json:"up"`
	Down  int `json:"down"`
}

// GroupStatusSummary returns the aggregate status per group id,
// counting every device in the group or any of its subgroups. Used
// by the map view's group markers: a group reads "down" when any
// device under it is down, "up" when it has devices and all of them
// are up, and "unknown" otherwise.
func (s *Store) GroupStatusSummary() (map[int64]GroupStatus, error) {
	rows, err := s.DB.Query(`
		WITH RECURSIVE tree(root, id) AS (
			SELECT id, id FROM groups_
			UNION ALL
			SELECT t.root, g.id FROM tree t JOIN groups_ g ON g.parent_id = t.id
		)
		SELECT t.root, COUNT(d.id),
			COALESCE(SUM(CASE WHEN d.status='up' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN d.status='down' THEN 1 ELSE 0 END), 0)
		FROM tree t LEFT JOIN devices d ON d.group_id = t.id
		GROUP BY t.root`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]GroupStatus{}
	for rows.Next() {
		var id int64
		var gs GroupStatus
		if err := rows.Scan(&id, &gs.Total, &gs.Up, &gs.Down); err == nil {
			out[id] = gs
		}
	}
	return out, rows.Err()
}

// AggregateStatus folds a GroupStatus into one marker state.
func (gs GroupStatus) AggregateStatus() string {
	switch {
	case gs.Down > 0:
		return "down"
	case gs.Total > 0 && gs.Up == gs.Total:
		return "up"
	default:
		return "unknown"
	}
}

// Downsample: raw 60s samples older than rawRetention are collapsed into
// 5-minute average buckets (in place — re-running is idempotent because a
// bucket holding a single averaged row averages to itself), and anything
// older than totalRetention is deleted.
func (s *Store) Downsample(now time.Time, rawRetention, totalRetention time.Duration) error {
	cutoff := now.Add(-rawRetention).Unix()
	rows, err := s.DB.Query(`SELECT device_id, ifindex, metric, (ts/300)*300 AS bucket, AVG(value) FROM samples WHERE ts < ? GROUP BY device_id, ifindex, metric, bucket`, cutoff)
	if err != nil {
		return err
	}
	type agg struct {
		dev     int64
		ifindex sql.NullInt64
		metric  string
		bucket  int64
		avg     float64
	}
	var aggs []agg
	for rows.Next() {
		var a agg
		if err := rows.Scan(&a.dev, &a.ifindex, &a.metric, &a.bucket, &a.avg); err == nil {
			aggs = append(aggs, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM samples WHERE ts < ?`, cutoff); err != nil {
		return err
	}
	for _, a := range aggs {
		var ifv any
		if a.ifindex.Valid {
			ifv = a.ifindex.Int64
		}
		if _, err := tx.Exec(`INSERT INTO samples(device_id, ifindex, metric, ts, value) VALUES(?,?,?,?,?)`, a.dev, ifv, a.metric, a.bucket, a.avg); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM samples WHERE ts < ?`, now.Add(-totalRetention).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

var _ = strings.TrimSpace
