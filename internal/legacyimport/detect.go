package legacyimport

// Direct Import support (v1.3): find the legacy monitoring system's
// SQL Server on the machine NetGold is running on, so the user does
// not have to type any connection details.
//
// Detection combines four sources:
//   - SQL Browser (SSRP) probes over UDP 1434 — lists the instances a
//     SQL Server machine is running, with their TCP ports.
//   - The Windows registry (Windows builds only, see detect_windows.go)
//     — the installed instance names.
//   - The Windows registry again — each instance's own TCP
//     configuration (static or dynamic port), so an instance can be
//     reached directly by port even when the SQL Browser service is
//     stopped (named instances of the SQL Server 2005 era typically
//     use dynamic ports and have the Browser service disabled).
//   - The default instance on TCP 1433.
//
// For every candidate we then try to authenticate WITHOUT guessing any
// password: Windows Authentication (Windows builds, the user NetGold is
// running as) and, when the NETGOLD_SQL_USER / NETGOLD_SQL_PASSWORD
// environment variables are set, that SQL login (a documented option
// for headless/server installs). If a connection works we fingerprint
// the server's databases and note whether one of them is the legacy
// monitoring database (see findLegacyDB).

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AutoTarget identifies one detected SQL Server endpoint. It carries
// connection coordinates only — never credentials — so the browser may
// hold it and post it back; the server re-derives the working
// connection (including which authentication succeeded) on every call.
type AutoTarget struct {
	Server   string `json:"server"`
	Instance string `json:"instance,omitempty"`
	Port     int    `json:"port,omitempty"`
}

func (t AutoTarget) key() string {
	return strings.ToLower(t.Server) + "|" + strings.ToLower(t.Instance) + "|" + fmt.Sprint(t.Port)
}

// Candidate is one detected SQL Server target, as returned by Detect.
type Candidate struct {
	DisplayName  string     `json:"display_name"`
	Target       AutoTarget `json:"target"`
	Source       string     `json:"source"` // "sql-browser", "registry", "registry-port" or "default"
	Reachable    bool       `json:"reachable"`
	CanConnect   bool       `json:"can_connect"`
	HasLegacyDB  bool       `json:"has_legacy_db"`
	LegacyDBName string     `json:"legacy_db_name,omitempty"`
	Detail       string     `json:"detail"`
	// ProbeSkip marks a candidate Detect must not probe: its Detail
	// already states a definitive local fact (for example that TCP/IP
	// is disabled for the instance in its registry configuration).
	ProbeSkip bool `json:"-"`
}

// InstancePortHint is one installed instance's TCP listening port,
// resolved straight from the instance's registry configuration — no
// SQL Browser round-trip and no instance-name resolution involved.
type InstancePortHint struct {
	Instance  string // instance name; "MSSQLSERVER" is the default instance
	ServiceID string // registry service key, e.g. "MSSQL.1"
	Port      int    // static or last dynamic port; 0 when none is recorded
	Dynamic   bool   // Port came from TcpDynamicPorts
	Disabled  bool   // TCP configuration exists but records no usable port
	Detail    string // explanation when Disabled
}

// ParseInstancePorts resolves instance TCP ports from raw registry
// values, given as plain maps so the logic is testable on any platform:
//
//	instanceNames: values of "...\Instance Names\SQL" (instance → service ID)
//	ipAll:         service ID → values of "...\<service ID>\MSSQLServer\
//	               SuperSocketNetLib\Tcp\IPAll"
//
// An instance whose service has no IPAll entry at all yields a hint
// with Port 0 and Disabled false ("no information") — the caller then
// keeps the classic instance-name target for it.
func ParseInstancePorts(instanceNames map[string]string, ipAll map[string]map[string]string) []InstancePortHint {
	names := make([]string, 0, len(instanceNames))
	for n := range instanceNames {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]InstancePortHint, 0, len(names))
	for _, name := range names {
		h := InstancePortHint{Instance: name, ServiceID: instanceNames[name]}
		vals, ok := ipAll[h.ServiceID]
		if !ok {
			out = append(out, h) // missing keys: no information
			continue
		}
		get := func(key string) string {
			for k, v := range vals {
				if strings.EqualFold(k, key) {
					return strings.TrimSpace(v)
				}
			}
			return ""
		}
		if p, err := strconv.Atoi(get("TcpPort")); err == nil && p > 0 {
			h.Port = p
		} else if p, err := strconv.Atoi(get("TcpDynamicPorts")); err == nil && p > 0 {
			h.Port, h.Dynamic = p, true
		} else {
			// The TCP configuration exists but records no usable port
			// (both values empty, or an explicit 0): TCP/IP is in
			// effect disabled for this instance.
			h.Disabled = true
			h.Detail = "TCP/IP appears disabled for this instance (its registry configuration records no TCP port)."
		}
		out = append(out, h)
	}
	return out
}

// SSRPInstance is one instance entry parsed from a SQL Browser response.
type SSRPInstance struct {
	ServerName   string
	InstanceName string
	Version      string
	TCPPort      int
}

// ParseSSRPResponse parses one SQL Browser (SSRP) response datagram.
// Wire format: byte 0 = 0x05, bytes 1-2 = little-endian length of the
// payload that follows, then per-instance blocks of semicolon-separated
// key;value pairs, each block terminated by an empty pair (";;").
func ParseSSRPResponse(datagram []byte) ([]SSRPInstance, error) {
	if len(datagram) < 3 {
		return nil, fmt.Errorf("response too short (%d bytes)", len(datagram))
	}
	if datagram[0] != 0x05 {
		return nil, fmt.Errorf("unexpected response type 0x%02x", datagram[0])
	}
	n := int(datagram[1]) | int(datagram[2])<<8
	payload := datagram[3:]
	if n < len(payload) {
		payload = payload[:n]
	}
	return ParseSSRPData(string(payload)), nil
}

// ParseSSRPData parses the text payload of an SSRP response.
func ParseSSRPData(data string) []SSRPInstance {
	var out []SSRPInstance
	for _, block := range strings.Split(data, ";;") {
		block = strings.Trim(block, ";")
		if block == "" {
			continue
		}
		parts := strings.Split(block, ";")
		kv := map[string]string{}
		for i := 0; i+1 < len(parts); i += 2 {
			kv[strings.ToLower(parts[i])] = parts[i+1]
		}
		inst := SSRPInstance{
			ServerName:   kv["servername"],
			InstanceName: kv["instancename"],
			Version:      kv["version"],
		}
		if p := kv["tcp"]; p != "" {
			fmt.Sscanf(p, "%d", &inst.TCPPort)
		}
		if inst.InstanceName == "" && inst.ServerName == "" {
			continue
		}
		out = append(out, inst)
	}
	return out
}

// QuerySSRP asks the SQL Browser service on host (UDP 1434) for its
// instance list, using the CLNT_UCAST_EX request (0x02 = all instances).
func QuerySSRP(host string, timeout time.Duration) ([]SSRPInstance, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, "1434"))
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte{0x02}); err != nil {
		return nil, err
	}
	var out []SSRPInstance
	seen := map[string]bool{}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			break // deadline reached or service absent: keep what we have
		}
		insts, err := ParseSSRPResponse(buf[:n])
		if err != nil {
			continue
		}
		for _, in := range insts {
			k := strings.ToLower(in.ServerName + "|" + in.InstanceName)
			if !seen[k] {
				seen[k] = true
				out = append(out, in)
			}
		}
	}
	return out, nil
}

// buildCandidates merges instance hints from the registry (names and
// resolved TCP ports) and from SSRP (plus the always-present
// default-instance candidate) into a deduped candidate list. Pure
// function — unit-tested.
func buildCandidates(registry []string, ssrp []SSRPInstance, hints []InstancePortHint) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	have1433 := false
	add := func(c Candidate) {
		k := c.Target.key()
		if seen[k] {
			return
		}
		seen[k] = true
		if c.Target.Port == 1433 {
			have1433 = true
		}
		out = append(out, c)
	}
	// SSRP first: it knows the real TCP ports.
	for _, in := range ssrp {
		name := in.ServerName
		if name == "" {
			name = "This computer"
		}
		disp := fmt.Sprintf("%s — instance %s", name, in.InstanceName)
		tgt := AutoTarget{Server: "127.0.0.1", Instance: in.InstanceName}
		if in.TCPPort > 0 {
			tgt = AutoTarget{Server: "127.0.0.1", Port: in.TCPPort}
			disp += fmt.Sprintf(" (port %d)", in.TCPPort)
		}
		add(Candidate{DisplayName: disp, Target: tgt, Source: "sql-browser"})
	}
	ssrpNames := map[string]bool{}
	for _, in := range ssrp {
		ssrpNames[strings.ToLower(in.InstanceName)] = true
	}
	// Registry TCP ports (Windows): a direct 127.0.0.1:<port> target
	// that needs neither the SQL Browser nor instance resolution. These
	// outrank the instance-name targets below, which are only added for
	// instances the port hints say nothing definitive about.
	hintByInst := map[string]InstancePortHint{}
	for _, h := range hints {
		hintByInst[strings.ToLower(h.Instance)] = h
		switch {
		case h.Port > 0:
			var disp string
			if strings.EqualFold(h.Instance, "MSSQLSERVER") {
				disp = fmt.Sprintf("This computer — SQL Server (default instance, port %d)", h.Port)
			} else {
				disp = fmt.Sprintf("This computer — instance %s (port %d)", h.Instance, h.Port)
			}
			add(Candidate{
				DisplayName: disp,
				Target:      AutoTarget{Server: "127.0.0.1", Port: h.Port},
				Source:      "registry-port",
			})
		case h.Disabled:
			var disp string
			var tgt AutoTarget
			if strings.EqualFold(h.Instance, "MSSQLSERVER") {
				disp = "This computer — SQL Server (default instance)"
				tgt = AutoTarget{Server: "127.0.0.1"}
			} else {
				disp = fmt.Sprintf("This computer — instance %s", h.Instance)
				tgt = AutoTarget{Server: "localhost", Instance: h.Instance}
			}
			add(Candidate{
				DisplayName: disp,
				Target:      tgt,
				Source:      "registry-port",
				Detail: "TCP/IP appears disabled for this instance. " +
					"Enable TCP/IP for the instance in SQL Server Configuration Manager, " +
					"or enter the server and its port manually in the Backup File card below.",
				ProbeSkip: true,
			})
		}
	}
	// Registry instances (Windows) without a definitive port hint: no
	// port knowledge — the driver resolves named instances through the
	// SQL Browser at connect time. Skip any instance SSRP already
	// reported: that route knows its port.
	for _, name := range registry {
		if ssrpNames[strings.ToLower(name)] {
			continue
		}
		if h, ok := hintByInst[strings.ToLower(name)]; ok && (h.Port > 0 || h.Disabled) {
			continue // already represented by its registry-port candidate
		}
		if strings.EqualFold(name, "MSSQLSERVER") {
			add(Candidate{
				DisplayName: "This computer — SQL Server (default instance, port 1433)",
				Target:      AutoTarget{Server: "127.0.0.1", Port: 1433},
				Source:      "registry",
			})
			continue
		}
		add(Candidate{
			DisplayName: fmt.Sprintf("This computer — instance %s", name),
			Target:      AutoTarget{Server: "localhost", Instance: name},
			Source:      "registry",
		})
	}
	// Default instance candidate, unless something already covers 1433.
	if !have1433 {
		add(Candidate{
			DisplayName: "This computer — SQL Server (default instance, port 1433)",
			Target:      AutoTarget{Server: "127.0.0.1", Port: 1433},
			Source:      "default",
		})
	}
	return out
}

func tcpReachable(host string, port int, timeout time.Duration) bool {
	if port <= 0 {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), timeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// tryConnect opens a connection and pings it under a context timeout.
func tryConnect(cfg ConnConfig, timeout time.Duration) (*SQLSource, error) {
	db, err := sql.Open("sqlserver", cfg.DSN())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLSource{DB: db}, nil
}

// authStyles returns the authentication attempts for an auto target, in
// order. No password is ever guessed: Windows Authentication uses the
// identity NetGold itself runs as (Windows only), and the SQL login
// attempt exists only when the operator configured NETGOLD_SQL_USER /
// NETGOLD_SQL_PASSWORD in NetGold's own environment.
func authAttempts() []func(*ConnConfig) {
	var out []func(*ConnConfig)
	if runtime.GOOS == "windows" {
		out = append(out, func(c *ConnConfig) { c.WindowsAuth = true })
	}
	if u := os.Getenv("NETGOLD_SQL_USER"); u != "" {
		p := os.Getenv("NETGOLD_SQL_PASSWORD")
		out = append(out, func(c *ConnConfig) { c.User, c.Password = u, p })
	}
	return out
}

// resolveTarget tries the authentication chain for a target and, on the
// first connection that works, fingerprints the server's databases. It
// returns the working connection config (database still "master" — the
// caller picks the database) and the legacy database's name ("" when
// the server holds none).
func resolveTarget(t AutoTarget) (ConnConfig, string, error) {
	attempts := authAttempts()
	if len(attempts) == 0 {
		return ConnConfig{}, "", fmt.Errorf("no authentication method is available on this computer (Windows Authentication is Windows-only, and NETGOLD_SQL_USER is not set) — please use the Advanced manual connection instead")
	}
	var lastErr error
	for _, apply := range attempts {
		cfg := ConnConfig{Server: t.Server, Instance: t.Instance, Port: t.Port, Database: "master"}
		apply(&cfg)
		src, err := tryConnect(cfg, 6*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		dbs, err := listDatabases(src.DB)
		if err != nil {
			src.Close()
			lastErr = err
			continue
		}
		legacy := findLegacyDB(src.DB, dbs)
		src.Close()
		return cfg, legacy, nil
	}
	return ConnConfig{}, "", fmt.Errorf("could not sign in to the SQL Server on this computer: %v", lastErr)
}

func listDatabases(db *sql.DB) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sys.databases ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// legacySignatureTables are the tables a database must contain to be
// the legacy monitoring database: its device inventory and its map
// views. Detection fingerprints the schema instead of trusting a
// database name, so a renamed legacy database is still found.
var legacySignatureTables = []string{"device", "mapview"}

// tablesContainLegacySignature reports whether tables (any case)
// include every signature table.
func tablesContainLegacySignature(tables []string) bool {
	have := map[string]bool{}
	for _, t := range tables {
		have[strings.ToLower(t)] = true
	}
	for _, want := range legacySignatureTables {
		if !have[want] {
			return false
		}
	}
	return true
}

// legacyDBFromTables picks, in the order given, the first database
// whose table list carries the legacy signature. System databases are
// never considered. Pure function — unit-tested.
func legacyDBFromTables(order []string, tablesByDB map[string][]string) string {
	for _, name := range order {
		switch strings.ToLower(name) {
		case "master", "model", "msdb", "tempdb":
			continue
		}
		if tablesContainLegacySignature(tablesByDB[name]) {
			return name
		}
	}
	return ""
}

// findLegacyDB fingerprints the databases listed in dbs (through the
// open server connection) and returns the legacy database's exact
// (original-case) name, or "".
func findLegacyDB(db *sql.DB, dbs []string) string {
	tablesByDB := map[string][]string{}
	for _, name := range dbs {
		q := "SELECT name FROM [" + strings.ReplaceAll(name, "]", "]]") + "].sys.tables"
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		var tables []string
		for rows.Next() {
			var tn string
			if err := rows.Scan(&tn); err == nil {
				tables = append(tables, tn)
			}
		}
		rows.Close()
		tablesByDB[name] = tables
	}
	return legacyDBFromTables(dbs, tablesByDB)
}

// ResolveAutoConn re-derives the working connection for an auto target
// and points it at the legacy database. Used by the import-auto flow
// and by the manual endpoints when they receive an auto target.
func ResolveAutoConn(t AutoTarget) (ConnConfig, error) {
	cfg, dbName, err := resolveTarget(t)
	if err != nil {
		return ConnConfig{}, err
	}
	if dbName == "" {
		return ConnConfig{}, fmt.Errorf("connected to the SQL Server, but no legacy monitoring database was found on it — is the legacy system installed on this computer?")
	}
	cfg.Database = dbName
	return cfg, nil
}

// ResolveAutoServerConn re-derives a working connection (pointed at
// the master database) for an auto target, without requiring a legacy
// database to exist. The .bak import uses it: it restores its own
// temporary database on the server.
func ResolveAutoServerConn(t AutoTarget) (ConnConfig, error) {
	cfg, _, err := resolveTarget(t)
	return cfg, err
}

// Detect finds SQL Server targets on this machine and checks each one:
// is it reachable, can we sign in without any typed credentials, and
// does it hold a legacy monitoring database?
func Detect() []Candidate {
	var ssrp []SSRPInstance
	hosts := []string{"127.0.0.1"}
	if hn, err := os.Hostname(); err == nil && hn != "" && !strings.EqualFold(hn, "localhost") {
		hosts = append(hosts, hn)
	}
	seenInst := map[string]bool{}
	for _, h := range hosts {
		insts, _ := QuerySSRP(h, 900*time.Millisecond)
		for _, in := range insts {
			k := strings.ToLower(in.ServerName + "|" + in.InstanceName)
			if !seenInst[k] {
				seenInst[k] = true
				ssrp = append(ssrp, in)
			}
		}
	}
	cands := buildCandidates(registryInstances(), ssrp, registryInstancePorts())
	for i := range cands {
		c := &cands[i]
		if c.ProbeSkip {
			continue // Detail already states the definitive local fact.
		}
		if c.Target.Port > 0 {
			c.Reachable = tcpReachable(c.Target.Server, c.Target.Port, 800*time.Millisecond)
		}
		// Only spend an authentication attempt where a server plausibly
		// is: a known port that answered, or a named instance (whose
		// dynamic port only the driver can resolve).
		if !c.Reachable && c.Target.Instance == "" {
			c.Detail = "No SQL Server answered on this port."
			continue
		}
		_, legacyName, err := resolveTarget(c.Target)
		if err != nil {
			if c.Reachable {
				c.Detail = "Server found, but sign-in failed — database not accessible without credentials. Use the Advanced manual connection below."
			} else {
				c.Detail = "Not reachable."
			}
			continue
		}
		c.Reachable = true
		c.CanConnect = true
		if legacyName != "" {
			c.HasLegacyDB = true
			c.LegacyDBName = legacyName
			c.Detail = "Legacy database found — ready to import."
		} else {
			c.Detail = "Server found and sign-in works, but it holds no legacy monitoring database."
		}
	}
	// Ready candidates first, then reachable ones, then the rest.
	sort.SliceStable(cands, func(i, j int) bool {
		score := func(c Candidate) int {
			switch {
			case c.HasLegacyDB:
				return 0
			case c.CanConnect:
				return 1
			case c.Reachable:
				return 2
			}
			return 3
		}
		return score(cands[i]) < score(cands[j])
	})
	return cands
}
