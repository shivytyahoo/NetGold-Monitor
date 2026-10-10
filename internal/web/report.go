package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"netgold/internal/report"
)

// parsePeriod reads ?from=<unix>&to=<unix> (seconds). Defaults: the last
// 7 days ending now. Returns an error string when the range is invalid.
func parsePeriod(r *http.Request) (time.Time, time.Time, string) {
	q := r.URL.Query()
	now := time.Now()
	from, to := now.Add(-7*24*time.Hour), now
	if v := q.Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return from, to, "invalid 'from' (want unix seconds)"
		}
		from = time.Unix(n, 0)
	}
	if v := q.Get("to"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return from, to, "invalid 'to' (want unix seconds)"
		}
		to = time.Unix(n, 0)
	}
	if !from.Before(to) {
		return from, to, "'from' must be before 'to'"
	}
	return from, to, ""
}

func (s *Server) downtimeReport(w http.ResponseWriter, r *http.Request) (report.Report, bool) {
	from, to, perr := parsePeriod(r)
	if perr != "" {
		errJSON(w, 400, perr)
		return report.Report{}, false
	}
	rep, err := report.FromStore(s.Store, from, to)
	if err != nil {
		errJSON(w, 500, err.Error())
		return report.Report{}, false
	}
	return rep, true
}

// GET /api/reports/downtime?from=<unix>&to=<unix> — JSON summary + details.
func (s *Server) downtimeJSON(w http.ResponseWriter, r *http.Request) {
	if rep, ok := s.downtimeReport(w, r); ok {
		writeJSON(w, 200, rep)
	}
}

// GET /api/reports/downtime.xlsx?from=<unix>&to=<unix>
func (s *Server) downtimeXLSX(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.downtimeReport(w, r)
	if !ok {
		return
	}
	var buf bytes.Buffer
	if err := report.WriteExcel(rep, &buf); err != nil {
		errJSON(w, 500, "excel: "+err.Error())
		return
	}
	name := fmt.Sprintf("netgold-downtime-%s_to_%s.xlsx",
		time.Unix(rep.From, 0).Format("2006-01-02"), time.Unix(rep.To, 0).Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.Write(buf.Bytes())
}

// GET /api/reports/downtime.pdf?from=<unix>&to=<unix>
func (s *Server) downtimePDF(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.downtimeReport(w, r)
	if !ok {
		return
	}
	var buf bytes.Buffer
	if err := report.WritePDF(rep, &buf); err != nil {
		errJSON(w, 500, "pdf: "+err.Error())
		return
	}
	name := fmt.Sprintf("netgold-downtime-%s_to_%s.pdf",
		time.Unix(rep.From, 0).Format("2006-01-02"), time.Unix(rep.To, 0).Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.Write(buf.Bytes())
}
