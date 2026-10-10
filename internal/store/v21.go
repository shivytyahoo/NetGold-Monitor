package store

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---- v2.1: flow aggregates (NetFlow / sFlow) ----

// FlowBucket is one aggregated minute of flow traffic.
type FlowBucket struct {
	Bucket   int64  `json:"bucket"`
	Exporter string `json:"exporter"`
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	DstPort  int    `json:"dst_port"`
	Proto    int    `json:"proto"`
	Bytes    uint64 `json:"bytes"`
	Packets  uint64 `json:"packets"`
}

// AddFlow folds one flow record into its 1-minute aggregate bucket.
func (s *Store) AddFlow(bucket int64, exporter, src, dst string, dstPort, proto int, bytes, packets uint64) error {
	_, err := s.DB.Exec(`INSERT INTO flow_buckets(bucket, exporter, src, dst, dst_port, proto, bytes, packets)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(bucket, exporter, src, dst, dst_port, proto) DO UPDATE SET
		bytes=bytes+excluded.bytes, packets=packets+excluded.packets`,
		bucket, exporter, src, dst, dstPort, proto, bytes, packets)
	return err
}

// FlowTop is one row of a "top talkers" style report.
type FlowTop struct {
	Key     string `json:"key"`
	Key2    string `json:"key2,omitempty"`
	Bytes   uint64 `json:"bytes"`
	Packets uint64 `json:"packets"`
}

// FlowTopN returns the biggest aggregates of the requested kind in
// [since, until]. kind: src | dst | conv | port | proto | exporter.
func (s *Store) FlowTopN(kind string, since, until int64, limit int) ([]FlowTop, error) {
	var cols, group string
	switch kind {
	case "src":
		cols, group = "src, ''", "src"
	case "dst":
		cols, group = "dst, ''", "dst"
	case "conv":
		cols, group = "src, dst", "src, dst"
	case "port":
		cols, group = "CAST(dst_port AS TEXT), ''", "dst_port"
	case "proto":
		cols, group = "CAST(proto AS TEXT), ''", "proto"
	case "exporter":
		cols, group = "exporter, ''", "exporter"
	default:
		return nil, fmt.Errorf("unknown flow report kind %q", kind)
	}
	rows, err := s.DB.Query(`SELECT `+cols+`, SUM(bytes), SUM(packets) FROM flow_buckets
		WHERE bucket>=? AND bucket<=? GROUP BY `+group+` ORDER BY SUM(bytes) DESC LIMIT ?`,
		since, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlowTop
	for rows.Next() {
		var t FlowTop
		if err := rows.Scan(&t.Key, &t.Key2, &t.Bytes, &t.Packets); err == nil {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// FlowSeries returns total bytes per bucket in [since, until], oldest first.
func (s *Store) FlowSeries(since, until int64) ([]Sample, error) {
	rows, err := s.DB.Query(`SELECT bucket, SUM(bytes) FROM flow_buckets WHERE bucket>=? AND bucket<=?
		GROUP BY bucket ORDER BY bucket`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var sm Sample
		var b uint64
		if err := rows.Scan(&sm.TS, &b); err == nil {
			sm.Value = float64(b)
			out = append(out, sm)
		}
	}
	return out, rows.Err()
}

// FlowTotals returns overall byte/packet totals in [since, until].
func (s *Store) FlowTotals(since, until int64) (bytes, packets uint64, err error) {
	var b, p *uint64
	err = s.DB.QueryRow(`SELECT SUM(bytes), SUM(packets) FROM flow_buckets WHERE bucket>=? AND bucket<=?`,
		since, until).Scan(&b, &p)
	if b != nil {
		bytes = *b
	}
	if p != nil {
		packets = *p
	}
	return bytes, packets, err
}

// ---- v2.1: syslog ----

// SeverityNames maps syslog severity numbers to their RFC names.
var SeverityNames = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// SeverityName returns the name for a severity number (0–7).
func SeverityName(n int) string {
	if n < 0 || n > 7 {
		return "info"
	}
	return SeverityNames[n]
}

type SyslogEntry struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	RecvTS   int64  `json:"recv_ts"`
	SourceIP string `json:"source_ip"`
	Hostname string `json:"hostname"`
	Facility int    `json:"facility"`
	Severity int    `json:"severity"`
	App      string `json:"app"`
	Message  string `json:"message"`
}

func (s *Store) AddSyslog(e SyslogEntry) error {
	if e.RecvTS == 0 {
		e.RecvTS = time.Now().Unix()
	}
	if e.TS == 0 {
		e.TS = e.RecvTS
	}
	_, err := s.DB.Exec(`INSERT INTO syslogs(ts, recv_ts, source_ip, hostname, facility, severity, app, message)
		VALUES(?,?,?,?,?,?,?,?)`, e.TS, e.RecvTS, e.SourceIP, e.Hostname, e.Facility, e.Severity, e.App, e.Message)
	return err
}

// SyslogQuery filters a syslog search. Severity -1 = any.
type SyslogQuery struct {
	Text     string
	Source   string
	Severity int
	Since    int64
	Until    int64
	Limit    int
}

func (s *Store) SearchSyslogs(q SyslogQuery) ([]SyslogEntry, error) {
	var conds []string
	var args []any
	if q.Text != "" {
		conds = append(conds, "(message LIKE ? OR hostname LIKE ? OR app LIKE ?)")
		like := "%" + q.Text + "%"
		args = append(args, like, like, like)
	}
	if q.Source != "" {
		conds = append(conds, "(source_ip=? OR hostname=?)")
		args = append(args, q.Source, q.Source)
	}
	if q.Severity >= 0 {
		conds = append(conds, "severity=?")
		args = append(args, q.Severity)
	}
	if q.Since > 0 {
		conds = append(conds, "recv_ts>=?")
		args = append(args, q.Since)
	}
	if q.Until > 0 {
		conds = append(conds, "recv_ts<=?")
		args = append(args, q.Until)
	}
	sqlStr := `SELECT id, ts, recv_ts, source_ip, hostname, facility, severity, app, message FROM syslogs`
	if len(conds) > 0 {
		sqlStr += " WHERE " + strings.Join(conds, " AND ")
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	sqlStr += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyslogEntry
	for rows.Next() {
		var e SyslogEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.RecvTS, &e.SourceIP, &e.Hostname, &e.Facility, &e.Severity, &e.App, &e.Message); err == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// SyslogSeverityCounts returns message counts per severity since a time.
func (s *Store) SyslogSeverityCounts(since int64) (map[int]int, error) {
	rows, err := s.DB.Query(`SELECT severity, COUNT(*) FROM syslogs WHERE recv_ts>=? GROUP BY severity`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var sev, n int
		if err := rows.Scan(&sev, &n); err == nil {
			out[sev] = n
		}
	}
	return out, rows.Err()
}

// ---- v2.1: discovered links ----

// DiscoveredLink is one neighbour relationship found by link discovery.
// DeviceA is always the lower device ID (links are stored normalised so
// the two directions of one physical link merge into a single row).
type DiscoveredLink struct {
	ID         int64  `json:"id"`
	DeviceA    int64  `json:"device_a"`
	IfA        string `json:"if_a"`
	DeviceB    int64  `json:"device_b"`
	IfB        string `json:"if_b"`
	Via        string `json:"via"`        // LLDP, CDP, FDB
	Confidence string `json:"confidence"` // high, medium, low
	LastSeen   int64  `json:"last_seen"`
	// Joined at read time:
	DeviceAName string `json:"device_a_name,omitempty"`
	DeviceBName string `json:"device_b_name,omitempty"`
}

// NormaliseLink orders a link's endpoints so A < B (swapping interfaces
// with them), making symmetric duplicates identical.
func NormaliseLink(l DiscoveredLink) DiscoveredLink {
	if l.DeviceB < l.DeviceA {
		l.DeviceA, l.DeviceB = l.DeviceB, l.DeviceA
		l.IfA, l.IfB = l.IfB, l.IfA
	}
	return l
}

func (s *Store) UpsertDiscoveredLink(l DiscoveredLink) error {
	l = NormaliseLink(l)
	if l.LastSeen == 0 {
		l.LastSeen = time.Now().Unix()
	}
	_, err := s.DB.Exec(`INSERT INTO discovered_links(device_a, if_a, device_b, if_b, via, confidence, last_seen)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(device_a, if_a, device_b, if_b, via) DO UPDATE SET confidence=excluded.confidence, last_seen=excluded.last_seen`,
		l.DeviceA, l.IfA, l.DeviceB, l.IfB, l.Via, l.Confidence, l.LastSeen)
	return err
}

func (s *Store) ListDiscoveredLinks() ([]DiscoveredLink, error) {
	rows, err := s.DB.Query(`SELECT l.id, l.device_a, l.if_a, l.device_b, l.if_b, l.via, l.confidence, l.last_seen,
		IFNULL(da.name,''), IFNULL(db.name,'')
		FROM discovered_links l
		LEFT JOIN devices da ON da.id=l.device_a
		LEFT JOIN devices db ON db.id=l.device_b
		ORDER BY l.device_a, l.device_b`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveredLink
	for rows.Next() {
		var l DiscoveredLink
		if err := rows.Scan(&l.ID, &l.DeviceA, &l.IfA, &l.DeviceB, &l.IfB, &l.Via, &l.Confidence, &l.LastSeen,
			&l.DeviceAName, &l.DeviceBName); err == nil {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

// PruneDiscoveredLinks drops links not seen since the given time.
func (s *Store) PruneDiscoveredLinks(before int64) (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM discovered_links WHERE last_seen < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneFlowsAndLogs applies the sample-style retention policy (90 days)
// to flow buckets and syslog entries.
func (s *Store) PruneFlowsAndLogs(now time.Time, retention time.Duration) error {
	cut := now.Add(-retention).Unix()
	if _, err := s.DB.Exec(`DELETE FROM flow_buckets WHERE bucket < ?`, cut); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM syslogs WHERE recv_ts < ?`, cut)
	return err
}

// SortLinks is a small helper for deterministic test output.
func SortLinks(ls []DiscoveredLink) {
	sort.Slice(ls, func(i, j int) bool {
		a, b := NormaliseLink(ls[i]), NormaliseLink(ls[j])
		if a.DeviceA != b.DeviceA {
			return a.DeviceA < b.DeviceA
		}
		if a.DeviceB != b.DeviceB {
			return a.DeviceB < b.DeviceB
		}
		return a.Via < b.Via
	})
}
