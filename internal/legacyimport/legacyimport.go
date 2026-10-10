// Package legacyimport imports configuration from a legacy network
// monitoring system's database.
//
// The legacy system stores everything in Microsoft SQL Server. This
// package connects live, discovers the schema from INFORMATION_SCHEMA,
// and imports groups, devices, interfaces, active monitor assignments
// and the network maps (diagrams).
//
// v1.1: the importer is now built and tested against a REAL legacy
// 14.3 Professional database (a real production backup, restored locally for testing). Key 14.3 facts it relies on:
//   - A device's IP address is NOT on the Device row; it lives on the
//     device's NetworkInterface row (sNetworkAddress), chosen via the
//     device's nDefaultNetworkInterfaceID.
//   - Device→group membership is PivotDeviceToGroup, not a Device column.
//   - Active monitors are assignments in PivotActiveMonitorTypeToDevice
//     (bRemoved=0) with names in ActiveMonitorType — v1 wrongly counted
//     the MonitorState table instead.
//   - Maps are MapView + Annotation (positions in the annotation XML).
//
// IMPORTANT, by design: the legacy system stores SNMP credentials
// encrypted. We never read or attempt to decrypt them — devices that
// had a legacy credential assignment (PivotCredentialTypeToDevice) are
// flagged needs-credentials and listed in the import report so the
// user re-enters the community once.
package legacyimport

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"netgold/internal/store"
)

type ConnConfig struct {
	Server      string `json:"server"`   // host or host\instance via Instance field
	Instance    string `json:"instance"` // optional named instance
	Port        int    `json:"port"`     // optional, default 1433
	Database    string `json:"database"` // the legacy database's name on the server
	User        string `json:"user"`
	Password    string `json:"password"`
	WindowsAuth bool   `json:"windows_auth"`
	// PipeName, when set, is a full named-pipe path to a SQL Server
	// instance (e.g. a LocalDB instance:
	// \\.\pipe\LOCALDB#XXXXXXXX\tsql\query). The connection then uses
	// go-mssqldb's named-pipe protocol. The sign-in is the SQL login
	// in User/Password when User is set (v3.0.1: NetGold's own engine
	// is reached with its dedicated provisioned login), otherwise
	// Windows Authentication. Set only by the automatic engine
	// setup; never accepted from the browser.
	PipeName string `json:"-"`
}

func (c ConnConfig) DSN() string {
	// No default database is assumed: callers name the legacy database
	// explicitly (the auto-detect flow finds it by its schema
	// fingerprint). An empty Database connects to the login's default
	// database on the server.
	db := c.Database
	if c.PipeName != "" {
		// ADO-style DSN: go-mssqldb's namedpipe dialer (Windows) treats
		// a server value starting with \\ as the full pipe path when
		// the protocol is np — no Browser query, no TCP port involved.
		if c.User != "" {
			// SQL login over the pipe (v3.0.1): NetGold's own LocalDB
			// engine is reached with the dedicated login the engine
			// setup provisioned — no SSPI handshake, so the import
			// does not depend on the Windows sign-in session having
			// usable credentials. The generated password's character
			// set excludes ';' and quotes, so this form is safe.
			return "server=np:" + c.PipeName + ";database=" + db + ";user id=" + c.User + ";password=" + c.Password
		}
		return "server=np:" + c.PipeName + ";database=" + db + ";trusted_connection=yes"
	}
	host := c.Server
	if c.Instance != "" {
		// go-mssqldb accepts instance in the host part of the URL
		host = c.Server + "\\" + c.Instance
	}
	port := c.Port
	if port == 0 {
		port = 1433
	}
	u := &url.URL{Scheme: "sqlserver", Host: fmt.Sprintf("%s:%d", host, port)}
	q := url.Values{}
	q.Set("database", db)
	if c.WindowsAuth {
		q.Set("trusted_connection", "yes")
	} else if c.User != "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ---------- Schema + row source abstraction (fake-able for tests) --------

type Schema struct {
	Tables map[string][]string // table name -> column names (original case)
}

func (s Schema) tableNames() []string {
	names := make([]string, 0, len(s.Tables))
	for t := range s.Tables {
		names = append(names, t)
	}
	sort.Strings(names)
	return names
}

// findTable picks a table by priority: exact (case-insensitive) first, then
// first containing `contains` while avoiding any of `avoid` substrings.
func (s Schema) findTable(exact string, contains string, avoid ...string) (string, bool) {
	for t := range s.Tables {
		if strings.EqualFold(t, exact) {
			return t, true
		}
	}
	best := ""
	for t := range s.Tables {
		lt := strings.ToLower(t)
		if !strings.Contains(lt, contains) {
			continue
		}
		skip := false
		for _, a := range avoid {
			if strings.Contains(lt, a) {
				skip = true
			}
		}
		if !skip && (best == "" || len(t) < len(best)) {
			best = t
		}
	}
	return best, best != ""
}

func (s Schema) has(table string) bool {
	for t := range s.Tables {
		if strings.EqualFold(t, table) {
			return true
		}
	}
	return false
}

type RowSource interface {
	Schema() (Schema, error)
	Rows(table string) ([]map[string]any, error)
	Close() error
}

// ---------- Live SQL Server source ----------

type SQLSource struct{ DB *sql.DB }

func Connect(cfg ConnConfig) (*SQLSource, error) {
	db, err := sql.Open("sqlserver", cfg.DSN())
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s", FormatConnectError(cfg, err, CurrentSessionContext()))
	}
	return &SQLSource{DB: db}, nil
}

// Ping opens a connection with cfg and closes it again: a bare
// reachability + sign-in probe with no session attached.
func Ping(cfg ConnConfig) error {
	src, err := tryConnect(cfg, 10*time.Second)
	if err != nil {
		return err
	}
	return src.Close()
}
func (s *SQLSource) Close() error { return s.DB.Close() }

func (s *SQLSource) Schema() (Schema, error) {
	rows, err := s.DB.Query(`SELECT TABLE_NAME, COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS ORDER BY TABLE_NAME, ORDINAL_POSITION`)
	if err != nil {
		return Schema{}, fmt.Errorf("reading schema: %w", err)
	}
	defer rows.Close()
	sch := Schema{Tables: map[string][]string{}}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err == nil {
			sch.Tables[t] = append(sch.Tables[t], c)
		}
	}
	if len(sch.Tables) == 0 {
		return sch, fmt.Errorf("no tables found — is this the legacy database?")
	}
	return sch, rows.Err()
}

func (s *SQLSource) Rows(table string) ([]map[string]any, error) {
	// table name comes from INFORMATION_SCHEMA discovery, quote it safely.
	q := `SELECT * FROM [` + strings.ReplaceAll(table, "]", "") + `]`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------- Mapping helpers (pure, unit-tested) ----------

// getCI fetches the first present column, case-insensitively.
func getCI(row map[string]any, names ...string) (any, bool) {
	lower := map[string]any{}
	for k, v := range row {
		lower[strings.ToLower(k)] = v
	}
	for _, n := range names {
		if v, ok := lower[strings.ToLower(n)]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

func strOf(v any) string {
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case []byte:
		return strings.TrimSpace(string(s))
	case nil:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func intOf(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case bool:
		if n {
			return 1
		}
		return 0
	case []byte:
		var x int64
		fmt.Sscanf(string(n), "%d", &x)
		return x
	case string:
		var x int64
		fmt.Sscanf(n, "%d", &x)
		return x
	}
	return 0
}

func truthy(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "1" || strings.EqualFold(b, "true")
	}
	return intOf(v) != 0
}

// ---------- Table resolution ----------

type resolved struct {
	sch                                                 Schema
	devT, grpT, ifT, devTypeT                           string
	devTypeCatT                                         string
	memberT, monPivotT, monTypeT, credPivotT            string
	mapViewT, annotT                                    string
	devOK, grpOK, ifOK, memberOK, monOK, credOK, mapsOK bool
}

func resolve(src RowSource) (resolved, error) {
	sch, err := src.Schema()
	if err != nil {
		return resolved{}, err
	}
	r := resolved{sch: sch}
	r.devT, r.devOK = sch.findTable("Device", "device", "group", "interface", "monitor", "discovery", "type", "attribute", "pivot")
	r.grpT, r.grpOK = sch.findTable("DeviceGroup", "group")
	r.ifT, r.ifOK = sch.findTable("NetworkInterface", "interface", "statistical", "physical", "device")
	// Secondary tables are matched exactly — heuristics here caused the v1
	// bug (MonitorState picked as "the monitor table").
	exact := func(name string) (string, bool) {
		for t := range sch.Tables {
			if strings.EqualFold(t, name) {
				return t, true
			}
		}
		return "", false
	}
	r.devTypeT, _ = exact("DeviceType")
	r.devTypeCatT, _ = exact("DeviceTypeCategory")
	r.memberT, r.memberOK = exact("PivotDeviceToGroup")
	r.monPivotT, _ = exact("PivotActiveMonitorTypeToDevice")
	r.monTypeT, _ = exact("ActiveMonitorType")
	r.monOK = r.monPivotT != ""
	r.credPivotT, r.credOK = exact("PivotCredentialTypeToDevice")
	r.mapViewT, _ = exact("MapView")
	r.annotT, _ = exact("Annotation")
	r.mapsOK = r.mapViewT != "" && r.annotT != ""
	return r, nil
}

// ---------- Dataset: everything loaded + joined in memory ----------
// All legacy config tables are small (hundreds of rows); loading them whole
// keeps the joins testable with a fake RowSource.

type niRow struct {
	id   int64
	addr string
	name string
}

type assignment struct {
	deviceOldID int64
	typeName    string
	argument    string
}

// deviceTypeInfo is one the legacy system DeviceType row: the display
// name legacy shows, the map icon file it draws, and its category.
type deviceTypeInfo struct {
	Name     string
	Icon     string // sMapViewIcon basename, e.g. "Juniper MX80.png"
	Category string // DeviceTypeCategory name, e.g. "Device Types (Basic)"
}

type dataset struct {
	r            resolved
	groupRows    []map[string]any
	deviceRows   []map[string]any // non-removed only
	removedCount int
	ifByDevice   map[int64][]niRow
	ipByDevice   map[int64]string
	memberByDev  map[int64]int64 // device old id -> group old id
	devTypes     map[int64]deviceTypeInfo
	monTypeNames map[int64]string
	assignments  []assignment // bRemoved=0 only
	credDevices  map[int64]bool
	mapViewRows  []map[string]any
	annotByMap   map[int64][]map[string]any
	annotCount   int
	warnings     []string
}

func loadDataset(src RowSource) (*dataset, error) {
	r, err := resolve(src)
	if err != nil {
		return nil, err
	}
	if !r.devOK {
		return nil, fmt.Errorf("no device table found in this database — this may not be a legacy monitoring database, or its schema version is very different")
	}
	ds := &dataset{
		r:            r,
		ifByDevice:   map[int64][]niRow{},
		ipByDevice:   map[int64]string{},
		memberByDev:  map[int64]int64{},
		devTypes:     map[int64]deviceTypeInfo{},
		monTypeNames: map[int64]string{},
		credDevices:  map[int64]bool{},
		annotByMap:   map[int64][]map[string]any{},
	}
	load := func(table string, ok bool, what string) []map[string]any {
		if !ok {
			return nil
		}
		rows, err := src.Rows(table)
		if err != nil {
			ds.warnings = append(ds.warnings, fmt.Sprintf("%s (table %s): %v", what, table, err))
			return nil
		}
		return rows
	}

	ds.groupRows = load(r.grpT, r.grpOK, "groups")
	devRows := load(r.devT, true, "devices")
	ifRows := load(r.ifT, r.ifOK, "interfaces")

	// Interfaces first: a device's IP comes from its NetworkInterface rows.
	for _, row := range ifRows {
		dv, ok := getCI(row, "nDeviceID", "DeviceID")
		if !ok {
			continue
		}
		var ni niRow
		if v, ok := getCI(row, "nNetworkInterfaceID", "NetworkInterfaceID", "ID"); ok {
			ni.id = intOf(v)
		}
		if v, ok := getCI(row, "sNetworkAddress", "NetworkAddress", "sIPAddress"); ok {
			ni.addr = strOf(v)
		}
		if v, ok := getCI(row, "sNetworkName", "NetworkName", "sName"); ok {
			ni.name = strOf(v)
		}
		if ni.addr == "" {
			ni.addr = ni.name // some rows carry the IP in the name field
		}
		ds.ifByDevice[intOf(dv)] = append(ds.ifByDevice[intOf(dv)], ni)
	}
	for _, row := range devRows {
		if v, ok := getCI(row, "bRemoved", "Removed"); ok && truthy(v) {
			ds.removedCount++
			continue
		}
		ds.deviceRows = append(ds.deviceRows, row)
		var devOld int64
		if v, ok := getCI(row, "nDeviceID", "DeviceID", "ID"); ok {
			devOld = intOf(v)
		}
		nis := ds.ifByDevice[devOld]
		if len(nis) == 0 {
			continue
		}
		// Prefer the device's default interface, else the first valid one.
		chosen := ""
		var defID int64 = -1
		if v, ok := getCI(row, "nDefaultNetworkInterfaceID", "DefaultNetworkInterfaceID"); ok {
			defID = intOf(v)
		}
		for _, ni := range nis {
			if ni.id == defID && net.ParseIP(ni.addr) != nil {
				chosen = ni.addr
				break
			}
		}
		if chosen == "" {
			for _, ni := range nis {
				if net.ParseIP(ni.addr) != nil {
					chosen = ni.addr
					break
				}
			}
		}
		if chosen != "" {
			ds.ipByDevice[devOld] = chosen
		}
	}
	// Drop interfaces of removed devices.
	alive := map[int64]bool{}
	for _, row := range ds.deviceRows {
		if v, ok := getCI(row, "nDeviceID", "DeviceID", "ID"); ok {
			alive[intOf(v)] = true
		}
	}
	for id := range ds.ifByDevice {
		if !alive[id] {
			delete(ds.ifByDevice, id)
		}
	}

	if r.memberOK {
		for _, row := range load(r.memberT, true, "group memberships") {
			dv, ok1 := getCI(row, "nDeviceID", "DeviceID")
			gv, ok2 := getCI(row, "nDeviceGroupID", "DeviceGroupID", "GroupID")
			if ok1 && ok2 {
				ds.memberByDev[intOf(dv)] = intOf(gv)
			}
		}
	}
	if r.devTypeT != "" {
		catNames := map[int64]string{}
		if r.devTypeCatT != "" {
			for _, row := range load(r.devTypeCatT, true, "device type categories") {
				if v, ok := getCI(row, "nDeviceTypeCategoryID", "DeviceTypeCategoryID", "ID"); ok {
					if nv, ok := getCI(row, "sCategoryName", "CategoryName", "sName", "Name"); ok {
						catNames[intOf(v)] = strOf(nv)
					}
				}
			}
		}
		for _, row := range load(r.devTypeT, true, "device types") {
			v, ok := getCI(row, "nDeviceTypeID", "DeviceTypeID", "ID")
			if !ok {
				continue
			}
			var info deviceTypeInfo
			if nv, ok := getCI(row, "sDisplayName", "DisplayName", "sName", "Name"); ok {
				info.Name = strOf(nv)
			}
			if iv, ok := getCI(row, "sMapViewIcon", "MapViewIcon", "sIcon", "Icon"); ok {
				info.Icon = strOf(iv)
			}
			if cv, ok := getCI(row, "nDeviceTypeCategoryID", "DeviceTypeCategoryID"); ok {
				info.Category = catNames[intOf(cv)]
			}
			ds.devTypes[intOf(v)] = info
		}
	}
	if r.monTypeT != "" {
		for _, row := range load(r.monTypeT, true, "monitor types") {
			if v, ok := getCI(row, "nActiveMonitorTypeID", "ActiveMonitorTypeID", "ID"); ok {
				if nv, ok := getCI(row, "sMonitorTypeName", "MonitorTypeName", "sName", "Name"); ok {
					ds.monTypeNames[intOf(v)] = strOf(nv)
				}
			}
		}
	}
	if r.monOK {
		for _, row := range load(r.monPivotT, true, "monitor assignments") {
			if v, ok := getCI(row, "bRemoved", "Removed"); ok && truthy(v) {
				continue
			}
			var a assignment
			if v, ok := getCI(row, "nDeviceID", "DeviceID"); ok {
				a.deviceOldID = intOf(v)
			}
			if v, ok := getCI(row, "nActiveMonitorTypeID", "ActiveMonitorTypeID"); ok {
				a.typeName = ds.monTypeNames[intOf(v)]
				if a.typeName == "" {
					a.typeName = fmt.Sprintf("Type #%d", intOf(v))
				}
			}
			if v, ok := getCI(row, "sArgument", "Argument"); ok {
				a.argument = strOf(v)
			}
			ds.assignments = append(ds.assignments, a)
		}
	}
	if r.credOK {
		// Counts only — credential VALUES are never read (they are in
		// CredentialTypeData, which this importer never queries).
		for _, row := range load(r.credPivotT, true, "credential assignments") {
			if v, ok := getCI(row, "nDeviceID", "DeviceID"); ok {
				ds.credDevices[intOf(v)] = true
			}
		}
	}
	if r.mapsOK {
		ds.mapViewRows = load(r.mapViewT, true, "maps")
		for _, row := range load(r.annotT, true, "map objects") {
			ds.annotCount++
			if v, ok := getCI(row, "nMapViewID", "MapViewID"); ok {
				ds.annotByMap[intOf(v)] = append(ds.annotByMap[intOf(v)], row)
			}
		}
	}
	return ds, nil
}

func (ds *dataset) monitorBreakdown() map[string]int {
	out := map[string]int{}
	for _, a := range ds.assignments {
		out[a.typeName]++
	}
	return out
}

func (ds *dataset) interfaceCount() int {
	n := 0
	for _, rows := range ds.ifByDevice {
		n += len(rows)
	}
	return n
}

// MapMonitor maps a legacy active-monitor type name (+ optional instance
// argument) to a NetGold check: kind (ping/snmp/service) and port.
// Port defaults: telnet 23, ftp 21, http 80, https 443 — an explicit
// numeric sArgument wins when present.
func MapMonitor(typeName, argument string) (kind string, port int) {
	lower := strings.ToLower(typeName)
	switch {
	case strings.Contains(lower, "ping"):
		return "ping", 0
	case strings.Contains(lower, "snmp"):
		return "snmp", 161
	}
	if p, err := strconv.Atoi(strings.TrimSpace(argument)); err == nil && p > 0 && p < 65536 {
		return "service", p
	}
	defaults := []struct {
		name string
		port int
	}{
		{"telnet", 23}, {"ftp", 21}, {"https", 443}, {"http", 80},
		{"ssh", 22}, {"smtp", 25}, {"dns", 53}, {"pop3", 110}, {"imap", 143},
	}
	for _, d := range defaults {
		if strings.Contains(lower, d.name) {
			return "service", d.port
		}
	}
	return "service", 0
}

type DeviceCandidate struct {
	OldID          int64
	Name           string
	IP             string
	TypeName       string
	TypeIcon       string // legacy sMapViewIcon basename for TypeName
	SnmpManageable bool
}

// MapDeviceRow maps one legacy Device row (+ its resolved IP, which lives on
// NetworkInterface in legacy) to a candidate. ok=false when the row is removed
// or has no usable IP.
func MapDeviceRow(row map[string]any, ip string) (DeviceCandidate, bool) {
	var d DeviceCandidate
	if v, ok := getCI(row, "bRemoved", "Removed"); ok && truthy(v) {
		return d, false
	}
	if net.ParseIP(ip) == nil {
		return d, false
	}
	d.IP = ip
	if v, ok := getCI(row, "nDeviceID", "DeviceID", "ID"); ok {
		d.OldID = intOf(v)
	}
	if v, ok := getCI(row, "sDisplayName", "DisplayName", "sName", "Name", "sDeviceName"); ok {
		d.Name = strOf(v)
	}
	if d.Name == "" {
		d.Name = ip
	}
	if v, ok := getCI(row, "bSnmpManageable", "SnmpManageable"); ok {
		d.SnmpManageable = truthy(v)
	}
	return d, true
}

// ---------- Preview ----------

type Preview struct {
	TablesFound       []string       `json:"tables_found"`
	DeviceTable       string         `json:"device_table"`
	GroupTable        string         `json:"group_table"`
	InterfaceTable    string         `json:"interface_table"`
	MonitorTable      string         `json:"monitor_table"`
	Groups            int            `json:"groups"`
	Devices           int            `json:"devices"`
	Interfaces        int            `json:"interfaces"`
	Monitors          int            `json:"monitors"`
	MonitorBreakdown  map[string]int `json:"monitor_breakdown"`
	Maps              int            `json:"maps"`
	MapObjects        int            `json:"map_objects"`
	CredentialDevices int            `json:"credential_devices"`
	Warnings          []string       `json:"warnings"`
}

func PreviewImport(src RowSource) (*Preview, error) {
	ds, err := loadDataset(src)
	if err != nil {
		return nil, err
	}
	r := ds.r
	p := &Preview{
		TablesFound: r.sch.tableNames(), DeviceTable: r.devT, GroupTable: r.grpT,
		InterfaceTable: r.ifT, MonitorTable: r.monPivotT,
		Groups: len(ds.groupRows), Devices: len(ds.deviceRows),
		Interfaces: ds.interfaceCount(), Monitors: len(ds.assignments),
		MonitorBreakdown: ds.monitorBreakdown(), Maps: len(ds.mapViewRows),
		MapObjects: ds.annotCount, Warnings: ds.warnings,
	}
	aliveCred := 0
	for _, row := range ds.deviceRows {
		if v, ok := getCI(row, "nDeviceID", "DeviceID", "ID"); ok && ds.credDevices[intOf(v)] {
			aliveCred++
		}
	}
	p.CredentialDevices = aliveCred
	if !r.grpOK {
		p.Warnings = append(p.Warnings, "No group table found — devices will import without groups.")
	}
	if !r.monOK {
		p.Warnings = append(p.Warnings, "No PivotActiveMonitorTypeToDevice table found — monitor assignments will not be imported.")
	}
	if !r.mapsOK {
		p.Warnings = append(p.Warnings, "No MapView/Annotation tables found — network maps will not be imported.")
	}
	noIP := len(ds.deviceRows) - len(ds.ipByDevice)
	if noIP > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d device(s) have no usable IP address on their NetworkInterface rows and will be skipped.", noIP))
	}
	return p, nil
}

// ---------- Import ----------

type Report struct {
	GroupsImported     int            `json:"groups_imported"`
	DevicesImported    int            `json:"devices_imported"`
	DevicesMerged      int            `json:"devices_merged"`
	DevicesSkipped     int            `json:"devices_skipped"`
	InterfacesImported int            `json:"interfaces_imported"`
	MonitorsFound      int            `json:"monitors_found"`
	MonitorsImported   int            `json:"monitors_imported"`
	MonitorBreakdown   map[string]int `json:"monitor_breakdown"`
	MapsImported       int            `json:"maps_imported"`
	MapObjectsImported int            `json:"map_objects_imported"`
	MapObjectsSkipped  int            `json:"map_objects_skipped"`
	NeedsCredentials   []string       `json:"needs_credentials"`
	Warnings           []string       `json:"warnings"`
}

func RunImport(src RowSource, st *store.Store) (*Report, error) {
	ds, err := loadDataset(src)
	if err != nil {
		return nil, err
	}
	rep := &Report{Warnings: ds.warnings, MonitorBreakdown: map[string]int{}}

	// --- Groups (hierarchy; note legacy root group has old id 0, parent NULL,
	// and children point AT 0 — so "parent id 0" is a real parent here).
	groupMap := map[int64]int64{}    // legacy group id -> our id
	groupNames := map[int64]string{} // legacy group id -> name
	{
		pending := ds.groupRows
		for pass := 0; pass <= len(pending)+1 && len(pending) > 0; pass++ {
			var next []map[string]any
			progress := false
			for _, row := range pending {
				nameV, ok := getCI(row, "sGroupName", "GroupName", "sName", "Name", "sDisplayName", "DisplayName")
				if !ok || strOf(nameV) == "" {
					continue
				}
				var idV int64
				if v, ok := getCI(row, "nDeviceGroupID", "DeviceGroupID", "GroupID", "ID", "nGroupID"); ok {
					idV = intOf(v)
				}
				var parent *int64
				if pv, ok := getCI(row, "nParentGroupID", "ParentGroupID", "nParentID", "ParentID"); ok {
					pid, ok2 := groupMap[intOf(pv)]
					if !ok2 {
						next = append(next, row) // parent not created yet
						continue
					}
					parent = &pid
				}
				id, err := st.AddGroup(strOf(nameV), parent)
				if err != nil {
					rep.Warnings = append(rep.Warnings, "group "+strOf(nameV)+": "+err.Error())
					continue
				}
				groupMap[idV] = id
				groupNames[idV] = strOf(nameV)
				rep.GroupsImported++
				progress = true
			}
			pending = next
			if !progress {
				break
			}
		}
		for _, row := range pending { // cyclic/orphan parents: import flat rather than lose them
			nameV, ok := getCI(row, "sGroupName", "GroupName", "sName", "Name")
			if !ok || strOf(nameV) == "" {
				continue
			}
			id, err := st.AddGroup(strOf(nameV), nil)
			if err != nil {
				continue
			}
			if v, ok := getCI(row, "nDeviceGroupID", "DeviceGroupID", "GroupID", "ID"); ok {
				groupMap[intOf(v)] = id
				groupNames[intOf(v)] = strOf(nameV)
			}
			rep.GroupsImported++
			rep.Warnings = append(rep.Warnings, "group "+strOf(nameV)+": parent group not found, imported at top level")
		}
	}

	// Devices with an SNMP monitor get SNMP polling even if legacy did not
	// mark them manageable.
	snmpMonitorDevs := map[int64]bool{}
	for _, a := range ds.assignments {
		if kind, _ := MapMonitor(a.typeName, a.argument); kind == "snmp" {
			snmpMonitorDevs[a.deviceOldID] = true
		}
	}

	// --- Devices. NetGold keys devices by IP; legacy allows several device
	// rows to share one IP (e.g. a district node and the switch at the same
	// address, in different groups). Such rows merge into ONE NetGold
	// device: first row wins for name/group/type, flags merge
	// (needs-credentials = OR, snmp-disabled = AND), and the twin's
	// monitors/interfaces still land on the merged device.
	devMap := map[int64]int64{}    // legacy device id -> our id
	ipOwner := map[string]int64{}  // IP -> our id (this import run)
	flagAcc := map[int64][2]bool{} // our id -> accumulated {needsCred, snmpDisabled}
	for _, row := range ds.deviceRows {
		var oldID int64
		if v, ok := getCI(row, "nDeviceID", "DeviceID", "ID"); ok {
			oldID = intOf(v)
		}
		cand, ok := MapDeviceRow(row, ds.ipByDevice[oldID])
		if !ok {
			rep.DevicesSkipped++
			continue
		}
		needsCred := ds.credDevices[oldID]
		snmpDisabled := !(cand.SnmpManageable || snmpMonitorDevs[oldID])
		if needsCred {
			rep.NeedsCredentials = append(rep.NeedsCredentials, fmt.Sprintf("%s (%s)", cand.Name, cand.IP))
		}
		// The device's the legacy system type (name + map icon), from its
		// DeviceType row — resolved for merged twins too, so a device
		// stored by an older import still gets backfilled below.
		var typeInfo deviceTypeInfo
		if v, ok := getCI(row, "nDeviceTypeID", "DeviceTypeID"); ok {
			typeInfo = ds.devTypes[intOf(v)]
		}
		if owner, dup := ipOwner[cand.IP]; dup {
			devMap[oldID] = owner
			rep.DevicesMerged++
			acc := flagAcc[owner]
			acc[0] = acc[0] || needsCred
			acc[1] = acc[1] && snmpDisabled
			flagAcc[owner] = acc
			if err := st.UpdateDeviceFlags(owner, acc[0], acc[1]); err != nil {
				rep.Warnings = append(rep.Warnings, "device "+cand.IP+" (merged): "+err.Error())
			}
			// Backfill only: the surviving device keeps the first
			// row's type (FillDeviceType never overwrites).
			if err := st.FillDeviceType(owner, typeInfo.Name, typeInfo.Icon); err != nil {
				rep.Warnings = append(rep.Warnings, "device "+cand.IP+" (merged, type): "+err.Error())
			}
			continue
		}
		typ := "unknown"
		if typeInfo.Name != "" {
			typ = typeInfo.Name
		}
		cand.TypeName = typ
		cand.TypeIcon = typeInfo.Icon
		var grp *int64
		if gOld, ok := ds.memberByDev[oldID]; ok {
			if gid, ok := groupMap[gOld]; ok {
				grp = &gid
			}
		}
		id, err := st.AddDevice(store.Device{
			Name: cand.Name, IP: cand.IP, GroupID: grp, Type: cand.TypeName,
			DeviceType: typeInfo.Name, TypeIcon: typeInfo.Icon,
			NeedsCredentials: needsCred, SnmpDisabled: snmpDisabled,
		})
		if err != nil {
			rep.DevicesSkipped++
			rep.Warnings = append(rep.Warnings, "device "+cand.IP+": "+err.Error())
			continue
		}
		devMap[oldID] = id
		ipOwner[cand.IP] = id
		flagAcc[id] = [2]bool{needsCred, snmpDisabled}
		rep.DevicesImported++
	}

	// --- Interfaces (each device's NetworkInterface rows; the device's
	// default interface gets ifindex 1). InterfacesImported counts rows
	// actually stored — merged duplicate-IP devices share one row.
	ifStored := map[[2]int64]bool{}
	for oldID, rows := range ds.ifByDevice {
		devID, ok := devMap[oldID]
		if !ok {
			continue
		}
		var defID int64 = -1
		for _, drow := range ds.deviceRows {
			if v, ok := getCI(drow, "nDeviceID", "DeviceID", "ID"); ok && intOf(v) == oldID {
				if dv, ok := getCI(drow, "nDefaultNetworkInterfaceID", "DefaultNetworkInterfaceID"); ok {
					defID = intOf(dv)
				}
			}
		}
		ordered := append([]niRow{}, rows...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].id == defID {
				return true
			}
			if ordered[j].id == defID {
				return false
			}
			return ordered[i].id < ordered[j].id
		})
		for i, ni := range ordered {
			name := ni.name
			if name == "" {
				name = ni.addr
			}
			key := [2]int64{devID, int64(i + 1)}
			if err := st.UpsertInterface(store.Interface{DeviceID: devID, IfIndex: i + 1, Name: name, OperStatus: "unknown"}); err == nil {
				if !ifStored[key] {
					ifStored[key] = true
					rep.InterfacesImported++
				}
			}
		}
	}

	// --- Monitor assignments.
	rep.MonitorsFound = len(ds.assignments)
	for _, a := range ds.assignments {
		devID, ok := devMap[a.deviceOldID]
		if !ok {
			continue
		}
		kind, port := MapMonitor(a.typeName, a.argument)
		inserted, err := st.AddDeviceMonitor(store.DeviceMonitor{DeviceID: devID, Kind: kind, Name: a.typeName, Port: port})
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("monitor %s on device %d: %v", a.typeName, a.deviceOldID, err))
			continue
		}
		if inserted {
			rep.MonitorsImported++
		}
		rep.MonitorBreakdown[a.typeName]++
	}

	// --- Maps (MapView + Annotation).
	if ds.r.mapsOK {
		for _, mv := range ds.mapViewRows {
			var mvID, gOld int64
			if v, ok := getCI(mv, "nMapViewID", "MapViewID", "ID"); ok {
				mvID = intOf(v)
			}
			if v, ok := getCI(mv, "nDeviceGroupID", "DeviceGroupID"); ok {
				gOld = intOf(v)
			}
			name := groupNames[gOld]
			if name == "" {
				name = fmt.Sprintf("Map %d", mvID)
			}
			var bounds ParsedMapView
			props := ""
			if v, ok := getCI(mv, "sDefinition", "Definition"); ok {
				props = strOf(v)
				if b, err := ParseMapView(props); err == nil {
					bounds = b
				} else {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("map %s: %v", name, err))
				}
			}
			var grp *int64
			if gid, ok := groupMap[gOld]; ok {
				grp = &gid
			}
			mapID, err := st.UpsertMap(store.Map{
				SourceMapID: mvID, Name: name, GroupID: grp, SourceGroupID: gOld,
				MinX: bounds.MinX, MinY: bounds.MinY, MaxX: bounds.MaxX, MaxY: bounds.MaxY,
				Props: props,
			})
			if err != nil {
				rep.Warnings = append(rep.Warnings, "map "+name+": "+err.Error())
				continue
			}
			rep.MapsImported++
			var objs []store.MapObject
			for _, ann := range ds.annotByMap[mvID] {
				defV, _ := getCI(ann, "sDefinition", "Definition")
				pa, err := ParseAnnotation(strOf(defV))
				if err != nil {
					rep.MapObjectsSkipped++
					continue
				}
				o := store.MapObject{
					Type: pa.Type, X: pa.X, Y: pa.Y, X2: pa.X2, Y2: pa.Y2,
					W: pa.W, H: pa.H, ZOrder: pa.ZOrder, Text: pa.Text,
					SourceGroupID: pa.SourceGroupID, Extra: pa.Extra,
				}
				if v, ok := getCI(ann, "nAnnotationID", "AnnotationID", "ID"); ok {
					o.SourceAnnotationID = intOf(v)
				}
				if v, ok := getCI(ann, "nDeviceID", "DeviceID"); ok && intOf(v) != 0 {
					o.SourceDeviceID = intOf(v)
					if did, ok := devMap[o.SourceDeviceID]; ok {
						o.DeviceID = &did
					}
				}
				if pa.SourceGroupID != 0 {
					if gid, ok := groupMap[pa.SourceGroupID]; ok {
						o.GroupID = &gid
					}
				}
				objs = append(objs, o)
			}
			if err := st.ReplaceMapObjects(mapID, objs); err != nil {
				rep.Warnings = append(rep.Warnings, "map objects "+name+": "+err.Error())
				continue
			}
			rep.MapObjectsImported += len(objs)
		}
	}

	if rep.DevicesMerged > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d legacy device(s) shared an IP address with another device and were merged into a single NetGold device (NetGold monitors one device per IP; the merged devices' monitors still apply).", rep.DevicesMerged))
	}
	if len(rep.NeedsCredentials) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d device(s) had credentials stored (encrypted) in the legacy system; they were flagged needs-credentials — re-enter their SNMP community once in NetGold.", len(rep.NeedsCredentials)))
	}
	if ds.removedCount > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d removed device(s) in the legacy database were not imported.", ds.removedCount))
	}
	// Automatic topologies (v3.6): a completed import is one of the
	// two moments they are generated (the other is the first start
	// of v3.6.0). Best effort — a failure here must not fail an
	// import that otherwise succeeded; it is reported as a warning.
	//
	// v3.6.1: generation is paused, like the first-start call in
	// the main program, while the Topology section is hidden from
	// the interface. The builder itself is unchanged; set this to
	// true when the section returns.
	const autoTopologiesEnabled = false
	if autoTopologiesEnabled {
		if auto, err := st.EnsureAutoTopologies(); err != nil {
			rep.Warnings = append(rep.Warnings, "automatic topologies could not be generated: "+err.Error())
		} else {
			if auto.HubBuilt {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("Automatic topology created: %s (%d nodes, %d design uplinks to the ASR 9K core).", auto.HubName, auto.HubNodes, auto.HubLinks))
			}
			if auto.CombinedBuilt {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("Automatic topology created: %s (%d nodes, %d links from the two maps' drawn lines).", auto.CombinedName, auto.CombinedNodes, auto.CombinedLinks))
			}
		}
	}
	return rep, nil
}
