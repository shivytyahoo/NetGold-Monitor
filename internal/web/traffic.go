package web

import (
	"net/http"
	"strconv"
	"time"

	"netgold/internal/flow"
	"netgold/internal/store"
)

// ---- Traffic (NetFlow / sFlow) ----

// trafficWindow resolves the shared range parameter to unix seconds.
func trafficWindow(r *http.Request) (since, until int64) {
	until = time.Now().Unix()
	rng := ranges[r.URL.Query().Get("range")]
	if rng == 0 {
		rng = time.Hour
	}
	return until - int64(rng/time.Second), until
}

func (s *Server) trafficSummary(w http.ResponseWriter, r *http.Request) {
	since, until := trafficWindow(r)
	top := func(kind string) []store.FlowTop {
		rows, _ := s.Store.FlowTopN(kind, since, until, 10)
		if rows == nil {
			return []store.FlowTop{}
		}
		return rows
	}
	bytes, packets, _ := s.Store.FlowTotals(since, until)
	series, _ := s.Store.FlowSeries(since, until)
	if series == nil {
		series = []store.Sample{}
	}
	// Protocol rows get display names.
	protos := top("proto")
	for i := range protos {
		if n, err := strconv.Atoi(protos[i].Key); err == nil {
			protos[i].Key = flow.ProtoName(n)
		}
	}
	writeJSON(w, 200, map[string]any{
		"total_bytes": bytes, "total_packets": packets,
		"top_sources": top("src"), "top_destinations": top("dst"),
		"top_conversations": top("conv"), "top_ports": top("port"),
		"top_protocols": protos, "top_exporters": top("exporter"),
		"series": series,
	})
}

func (s *Server) trafficRecent(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if s.Flows == nil {
		writeJSON(w, 200, []flow.Record{})
		return
	}
	writeJSON(w, 200, s.Flows.Recent(limit))
}

func (s *Server) trafficStatus(w http.ResponseWriter, r *http.Request) {
	if s.Flows == nil {
		writeJSON(w, 200, map[string]any{"listeners": []string{}, "packets": 0, "records": 0, "bad_packets": 0, "counter_samples": 0})
		return
	}
	p, rec, bad, counters := s.Flows.Stats()
	writeJSON(w, 200, map[string]any{
		"listeners": s.Flows.Listeners(), "packets": p, "records": rec,
		"bad_packets": bad, "counter_samples": counters,
	})
}

// ---- Logs (syslog) ----

func (s *Server) logsQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sq := store.SyslogQuery{Text: q.Get("q"), Source: q.Get("source"), Severity: -1, Limit: 200}
	if v := q.Get("severity"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 7 {
			sq.Severity = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sq.Limit = n
		}
	}
	rng := ranges[q.Get("range")]
	if rng == 0 {
		rng = 24 * time.Hour
	}
	sq.Since = time.Now().Add(-rng).Unix()
	entries, err := s.Store.SearchSyslogs(sq)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	type logOut struct {
		store.SyslogEntry
		SeverityName string `json:"severity_name"`
	}
	out := make([]logOut, 0, len(entries))
	for _, e := range entries {
		out = append(out, logOut{e, store.SeverityName(e.Severity)})
	}
	writeJSON(w, 200, out)
}

func (s *Server) logsCounts(w http.ResponseWriter, r *http.Request) {
	rng := ranges[r.URL.Query().Get("range")]
	if rng == 0 {
		rng = 24 * time.Hour
	}
	counts, err := s.Store.SyslogSeverityCounts(time.Now().Add(-rng).Unix())
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	byName := map[string]int{}
	total := 0
	for sev, n := range counts {
		byName[store.SeverityName(sev)] = n
		total += n
	}
	received := uint64(0)
	if s.Syslog != nil {
		received = s.Syslog.Received()
	}
	writeJSON(w, 200, map[string]any{"by_severity": byName, "total": total,
		"received_since_start": received, "port": s.SyslogPort()})
}

// SyslogPort reports the configured syslog port for display (0 = off).
func (s *Server) SyslogPort() int {
	if s.Syslog == nil {
		return 0
	}
	return s.Syslog.Port
}
