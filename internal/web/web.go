// Package web serves NetGold's embedded dashboard and JSON API.
// No external CDN: all assets (including uPlot) are vendored and embedded.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"netgold/internal/discovery"
	"netgold/internal/flow"
	"netgold/internal/legacyimport"
	"netgold/internal/poller"
	"netgold/internal/sqlengine"
	"netgold/internal/store"
	"netgold/internal/syslogsrv"
)

// Version is the NetGold release served by this build.
const Version = "3.7.0"

// versionToken is the placeholder in the embedded index page that is
// replaced with Version when the page is served (v2.2.2). The page
// compares its own build against /api/version so a stale tab left over
// from an older program can warn instead of silently showing a dead
// or outdated interface.
const versionToken = "__NETGOLD_VERSION__"

// maskSecret is what the API returns in place of a stored password. On a
// write, receiving this value back means "keep the stored password".
const maskSecret = "********"

// maskedDevice returns a copy of the device with SNMP v3 passwords masked.
func maskedDevice(d store.Device) store.Device {
	if d.V3AuthPass != "" {
		d.V3AuthPass = maskSecret
	}
	if d.V3PrivPass != "" {
		d.V3PrivPass = maskSecret
	}
	return d
}

//go:embed assets
var assets embed.FS

type Server struct {
	Store  *store.Store
	Engine *poller.Engine
	// v2.1 collectors (may be nil in tests; handlers tolerate nil).
	Flows  *flow.Collector
	Syslog *syslogsrv.Server
	// RunningAsService is true when this process is the Windows
	// service (v2.1 service mode). Automatic engine setup refuses to
	// run there: a service cannot show installer/UAC prompts.
	RunningAsService bool

	bakSetupMu sync.Mutex
	bakSetup   *sqlengine.Setup

	// Sign-in state (v3.4, see auth.go): server-side sessions and
	// the login throttle, initialised lazily by the auth guard.
	authMu    sync.Mutex
	sessions  map[string]*authSession
	throttles map[string]*loginThrottle
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(assets, "assets")
	fileServer := noStore(http.FileServer(http.FS(sub)))
	// The index page is served through serveIndex so the build version
	// is stamped into it (v2.2.2): the page's meta tag, sidebar footer
	// and About line all carry the version of the program that served
	// it, and the page script cross-checks it against /api/version.
	// The Topology section is hidden for now (v3.6.1): there is no
	// tab for it, and a direct visit to its old address lands on
	// the Dashboard. All topology code, APIs and data stay in
	// place untouched — only the door is closed.
	topologyHidden := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	}
	mux.HandleFunc("GET /topology", topologyHidden)
	mux.HandleFunc("GET /topology/{$}", topologyHidden)

	index := loadIndexPage()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": Version})
	})
	// Sign-in and roles (v3.4, auth.go). The login page and its
	// endpoints are the only session-free routes; everything else is
	// gated centrally by authGuard below.
	mux.HandleFunc("GET /login", s.serveLoginPage)
	mux.HandleFunc("POST /api/auth/setup", s.authSetup)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/logout", s.authLogout)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("GET /api/auth/me", s.authMe)
	mux.HandleFunc("GET /api/settings/access", s.getAccessSettings)
	mux.HandleFunc("PUT /api/settings/access", s.putAccessSettings)
	mux.HandleFunc("PUT /api/settings/access/admin-password", s.putAdminPassword)
	// Tidy view layout computation (v3.4, tidy.go): a pure function
	// of its request body — nothing is stored.
	mux.HandleFunc("POST /api/tidy", s.tidyLayout)

	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("POST /api/devices", s.addDevice)
	mux.HandleFunc("GET /api/devices/{id}", s.getDevice)
	mux.HandleFunc("PUT /api/devices/{id}", s.updateDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.deleteDevice)
	mux.HandleFunc("POST /api/devices/{id}/poll", s.pollNow)
	mux.HandleFunc("GET /api/devices/{id}/interfaces", s.interfaces)
	mux.HandleFunc("GET /api/groups", s.listGroups)
	mux.HandleFunc("POST /api/groups", s.addGroup)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/top", s.top)
	mux.HandleFunc("GET /api/graph", s.graph)
	mux.HandleFunc("GET /api/devices/{id}/timeline", s.deviceTimeline)
	mux.HandleFunc("GET /api/devices/{id}/timeline.xlsx", s.deviceTimelineXLSX)
	mux.HandleFunc("GET /api/devices/{id}/timeline.pdf", s.deviceTimelinePDF)
	mux.HandleFunc("GET /api/devices/{id}/monitors", s.deviceMonitors)
	mux.HandleFunc("POST /api/devices/{id}/monitors", s.addMonitor)
	mux.HandleFunc("DELETE /api/devices/{id}/monitors/{mid}", s.deleteMonitor)
	mux.HandleFunc("GET /api/maps", s.listMaps)
	mux.HandleFunc("GET /api/maps/{id}", s.getMap)
	// v3.7: map editing (see mapedit.go) — administrator only; the
	// auth guard refuses every mutating method for viewers.
	mux.HandleFunc("POST /api/maps/{id}/objects", s.addMapObject)
	mux.HandleFunc("PUT /api/maps/{id}/objects/{oid}", s.moveMapObject)
	mux.HandleFunc("DELETE /api/maps/{id}/objects/{oid}", s.deleteMapObject)
	// v2.0: topologies (see topology.go).
	mux.HandleFunc("GET /api/topologies", s.listTopologies)
	mux.HandleFunc("POST /api/topologies", s.createTopology)
	mux.HandleFunc("GET /api/topologies/{id}", s.getTopology)
	mux.HandleFunc("PUT /api/topologies/{id}", s.renameTopology)
	mux.HandleFunc("DELETE /api/topologies/{id}", s.deleteTopology)
	mux.HandleFunc("POST /api/topologies/{id}/nodes", s.addTopologyNode)
	mux.HandleFunc("PUT /api/topologies/{id}/nodes/{nid}", s.updateTopologyNode)
	mux.HandleFunc("DELETE /api/topologies/{id}/nodes/{nid}", s.deleteTopologyNode)
	mux.HandleFunc("POST /api/topologies/{id}/links", s.addTopologyLink)
	mux.HandleFunc("DELETE /api/topologies/{id}/links/{lid}", s.deleteTopologyLink)
	mux.HandleFunc("POST /api/topologies/from-map", s.topologyFromMap)
	mux.HandleFunc("POST /api/topologies/auto/rebuild", s.rebuildAutoTopologies)
	mux.HandleFunc("POST /api/topologies/import", s.topologyImport)
	mux.HandleFunc("GET /api/topologies/{id}/export.xlsx", s.topologyExport)
	mux.HandleFunc("GET /api/topology/template.xlsx", s.topologyTemplate)
	// v2.0: email alert settings (see settings.go).
	mux.HandleFunc("GET /api/settings/email", s.getEmailSettings)
	mux.HandleFunc("PUT /api/settings/email", s.putEmailSettings)
	mux.HandleFunc("POST /api/settings/email/test", s.testEmail)
	mux.HandleFunc("GET /api/reports/downtime", s.downtimeJSON)
	mux.HandleFunc("GET /api/reports/downtime.xlsx", s.downtimeXLSX)
	mux.HandleFunc("GET /api/reports/downtime.pdf", s.downtimePDF)
	// v2.1: traffic (NetFlow/sFlow), logs (syslog), discovered links.
	mux.HandleFunc("GET /api/traffic/summary", s.trafficSummary)
	mux.HandleFunc("GET /api/traffic/recent", s.trafficRecent)
	mux.HandleFunc("GET /api/traffic/status", s.trafficStatus)
	mux.HandleFunc("GET /api/logs", s.logsQuery)
	mux.HandleFunc("GET /api/logs/counts", s.logsCounts)
	mux.HandleFunc("GET /api/links", s.listLinks)
	mux.HandleFunc("GET /api/links/status", s.linksStatus)
	mux.HandleFunc("POST /api/links/discover", s.runLinkDiscovery)
	mux.HandleFunc("POST /api/links/topology", s.topologyFromLinks)
	mux.HandleFunc("POST /api/discover", s.discover)
	mux.HandleFunc("POST /api/legacy/preview", s.legacyPreview)
	mux.HandleFunc("POST /api/legacy/import", s.legacyImport)
	mux.HandleFunc("GET /api/legacy/detect", s.legacyDetect)
	mux.HandleFunc("POST /api/legacy/import-auto", s.legacyImportAuto)
	mux.HandleFunc("POST /api/legacy/import-bak", s.legacyImportBak)
	// v2.2: automatic SQL engine setup for backup imports.
	mux.HandleFunc("GET /api/legacy/bak-engine", s.legacyBakEngine)
	mux.HandleFunc("POST /api/legacy/bak-setup", s.legacyBakSetup)
	mux.HandleFunc("POST /api/legacy/bak-repair", s.legacyBakRepair)
	mux.HandleFunc("GET /api/legacy/bak-setup/status", s.legacyBakSetupStatus)
	mux.HandleFunc("POST /api/legacy/bak-engine/remove-instance", s.legacyBakRemoveInstance)
	return s.authGuard(mux)
}

func (s *Server) deviceMonitors(w http.ResponseWriter, r *http.Request) {
	mons, err := s.Store.ListDeviceMonitors(pathID(r))
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if mons == nil {
		mons = []store.DeviceMonitor{}
	}
	writeJSON(w, 200, mons)
}

func (s *Server) listMaps(w http.ResponseWriter, r *http.Request) {
	maps, err := s.Store.ListMaps()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if maps == nil {
		maps = []store.Map{}
	}
	writeJSON(w, 200, maps)
}

func (s *Server) getMap(w http.ResponseWriter, r *http.Request) {
	m, err := s.Store.GetMap(pathID(r))
	if err != nil {
		errJSON(w, 404, "map not found")
		return
	}
	objs, err := s.Store.ListMapObjects(m.ID)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if objs == nil {
		objs = []store.MapObject{}
	}
	// Resolve the drawn lines against the map's devices (v3.1): legacy
	// stores lines as geometry only, so the device association is
	// reconstructed geometrically and labelled as such in the UI.
	resolved := store.ResolveMapLines(objs)
	for i := range objs {
		if objs[i].Type != "Line" {
			continue
		}
		if r, ok := resolved[objs[i].ID]; ok {
			objs[i].LineFromDevice = r.FromDevice
			objs[i].LineToDevice = r.ToDevice
			objs[i].LineState = r.State
			objs[i].DrawX, objs[i].DrawY = r.X1, r.Y1
			objs[i].DrawX2, objs[i].DrawY2 = r.X2, r.Y2
		}
	}
	// DeviceGroup objects carry their group's aggregate status
	// (v3.6), so the overview maps can draw a group marker with the
	// group's live state instead of a bare box.
	if summary, err := s.Store.GroupStatusSummary(); err == nil {
		for i := range objs {
			if objs[i].Type != "DeviceGroup" || objs[i].GroupID == nil {
				continue
			}
			if gs, ok := summary[*objs[i].GroupID]; ok {
				objs[i].GroupStatus = gs.AggregateStatus()
				objs[i].GroupDevices = gs.Total
			}
		}
	}
	writeJSON(w, 200, map[string]any{"map": m, "objects": objs})
}

// noStore marks every embedded-asset response uncacheable. The
// single-page app served from these assets IS the product logic: an
// upgraded program must never be paired with a stale cached copy of
// its own interface (v2.2.1 — a cached pre-v2.2 app.js kept running
// against the v2.2 program and hid its new Import guidance). The
// assets are small and served locally, so re-fetching them on every
// load costs nothing that matters.
// loadIndexPage reads the embedded index page once and stamps the
// build version into every versionToken placeholder.
func loadIndexPage() []byte {
	raw, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return []byte("NetGold Monitor")
	}
	return bytes.ReplaceAll(raw, []byte(versionToken), []byte(Version))
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		errJSON(w, 400, "bad json: "+err.Error())
		return false
	}
	return true
}
func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	total, up, down, disabled, err := s.Store.CountsDetailed()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	devs, _ := s.Store.ListDevices()
	var downList []store.Device
	for _, d := range devs {
		if d.Status == "down" && !d.MonitoringDisabled {
			downList = append(downList, d)
		}
	}
	if downList == nil {
		downList = []store.Device{}
	}
	writeJSON(w, 200, map[string]any{"total": total, "up": up, "down": down, "disabled": disabled, "down_devices": downList})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.Store.ListDevices()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if devs == nil {
		devs = []store.Device{}
	}
	// attach latest ping for the list view
	type devOut struct {
		store.Device
		LatencyMs *float64 `json:"latency_ms,omitempty"`
		UptimeS   *float64 `json:"uptime_s,omitempty"`
	}
	out := make([]devOut, 0, len(devs))
	for _, d := range devs {
		o := devOut{Device: maskedDevice(d)}
		if v, ok := s.Store.LatestSample(d.ID, -2, "ping_ms"); ok {
			o.LatencyMs = &v
		}
		if v, ok := s.Store.LatestSample(d.ID, -2, "uptime_s"); ok {
			o.UptimeS = &v
		}
		out = append(out, o)
	}
	writeJSON(w, 200, out)
}

func (s *Server) addDevice(w http.ResponseWriter, r *http.Request) {
	var d store.Device
	if !decode(w, r, &d) {
		return
	}
	if d.IP == "" {
		errJSON(w, 400, "ip is required")
		return
	}
	if d.Name == "" {
		d.Name = d.IP
	}
	// A freshly submitted masked placeholder means "no password".
	if d.V3AuthPass == maskSecret {
		d.V3AuthPass = ""
	}
	if d.V3PrivPass == maskSecret {
		d.V3PrivPass = ""
	}
	id, err := s.Store.AddDevice(d)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	dev, err := s.Store.GetDevice(id)
	if err != nil {
		errJSON(w, 500, "device saved but reload failed: "+err.Error())
		return
	}
	writeJSON(w, 201, maskedDevice(*dev))
	go s.Engine.PollDevice(context.Background(), dev)
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	dev, err := s.Store.GetDevice(pathID(r))
	if err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	writeJSON(w, 200, maskedDevice(*dev))
}

// updateDevice saves edits from the device form (name, IP address,
// group, type, the SNMP credential fields incl. v3, and the
// monitoring on/off switch). A masked password field means "keep
// the stored password"; an empty field clears it. The IP address is
// editable (v3.6): it must stay unique — taking another device's
// address is refused with a clear message.
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	existing, err := s.Store.GetDevice(id)
	if err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	var d store.Device
	if !decode(w, r, &d) {
		return
	}
	d.ID = id
	d.IP = strings.TrimSpace(d.IP)
	if d.IP == "" {
		d.IP = existing.IP
	}
	if net.ParseIP(d.IP) == nil {
		errJSON(w, 400, "That is not a valid IP address: "+d.IP)
		return
	}
	if d.Name == "" {
		d.Name = existing.Name
	}
	if d.V3AuthPass == maskSecret {
		d.V3AuthPass = existing.V3AuthPass
	}
	if d.V3PrivPass == maskSecret {
		d.V3PrivPass = existing.V3PrivPass
	}
	// An SNMP answer clears the re-entry flag: the user has supplied
	// credentials again.
	if d.Community != "" || d.SNMPVersion == "v3" {
		d.NeedsCredentials = false
	}
	if err := s.Store.UpdateDevice(d); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			errJSON(w, 409, "Another device already uses the IP address "+d.IP+". Each device must have its own address.")
			return
		}
		errJSON(w, 500, err.Error())
		return
	}
	dev, err := s.Store.GetDevice(id)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, maskedDevice(*dev))
	if !dev.MonitoringDisabled {
		go s.Engine.PollDevice(context.Background(), dev)
	}
}

func (s *Server) addMonitor(w http.ResponseWriter, r *http.Request) {
	devID := pathID(r)
	if _, err := s.Store.GetDevice(devID); err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	var m store.DeviceMonitor
	if !decode(w, r, &m) {
		return
	}
	m.DeviceID = devID
	if m.Name == "" {
		errJSON(w, 400, "monitor name is required")
		return
	}
	if m.Kind == "" {
		m.Kind = "service"
	}
	if _, err := s.Store.AddDeviceMonitor(m); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	mons, err := s.Store.ListDeviceMonitors(devID)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, mons)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	mid, _ := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err := s.Store.DeleteDeviceMonitor(mid); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteDevice(pathID(r)); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) pollNow(w http.ResponseWriter, r *http.Request) {
	dev, err := s.Store.GetDevice(pathID(r))
	if err != nil {
		errJSON(w, 404, "device not found")
		return
	}
	go s.Engine.PollDevice(context.Background(), dev)
	writeJSON(w, 202, map[string]any{"ok": true, "message": "poll started"})
}

func (s *Server) interfaces(w http.ResponseWriter, r *http.Request) {
	ifs, err := s.Store.ListInterfaces(pathID(r))
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if ifs == nil {
		ifs = []store.Interface{}
	}
	// attach latest bandwidth per interface
	type ifOut struct {
		store.Interface
		InBps  *float64 `json:"in_bps,omitempty"`
		OutBps *float64 `json:"out_bps,omitempty"`
	}
	out := make([]ifOut, 0, len(ifs))
	for _, i := range ifs {
		o := ifOut{Interface: i}
		if v, ok := s.Store.LatestSample(i.DeviceID, i.IfIndex, "if_in_bps"); ok {
			o.InBps = &v
		}
		if v, ok := s.Store.LatestSample(i.DeviceID, i.IfIndex, "if_out_bps"); ok {
			o.OutBps = &v
		}
		out = append(out, o)
	}
	writeJSON(w, 200, out)
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Store.ListGroups()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if gs == nil {
		gs = []store.Group{}
	}
	writeJSON(w, 200, gs)
}

func (s *Server) addGroup(w http.ResponseWriter, r *http.Request) {
	var g store.Group
	if !decode(w, r, &g) {
		return
	}
	id, err := s.Store.AddGroup(g.Name, g.ParentID)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "name": g.Name})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Alarm polling mode: ?since=<unix ts> or the precise ?since_id=<event id>
	// cursor returns events newer than the cursor, oldest first. Without a
	// cursor it returns the latest 50, newest first (dashboard behaviour).
	if q.Has("since") || q.Has("since_id") {
		sinceID, _ := strconv.ParseInt(q.Get("since_id"), 10, 64)
		sinceTS, _ := strconv.ParseInt(q.Get("since"), 10, 64)
		limit := 200
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		evs, err := s.Store.EventsSince(sinceID, sinceTS, limit)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if evs == nil {
			evs = []store.Event{}
		}
		writeJSON(w, 200, evs)
		return
	}
	// Per-device mode: ?device=<id> returns that device's recent
	// events of every kind, newest first (device detail page).
	if q.Has("device") {
		devID, _ := strconv.ParseInt(q.Get("device"), 10, 64)
		limit := 50
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		evs, err := s.Store.DeviceEvents(devID, limit)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if evs == nil {
			evs = []store.Event{}
		}
		writeJSON(w, 200, evs)
		return
	}
	evs, err := s.Store.ListEvents(50)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if evs == nil {
		evs = []store.Event{}
	}
	writeJSON(w, 200, evs)
}

func (s *Server) top(w http.ResponseWriter, r *http.Request) {
	top, err := s.Store.TopInterfaces(10)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if top == nil {
		top = []store.TopInterface{}
	}
	writeJSON(w, 200, top)
}

var ranges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// GET /api/graph?device=1&kind=bandwidth&ifindex=1&range=6h
// kind: bandwidth | ping_ms | cpu_pct | mem_pct | ping_loss
func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	devID, _ := strconv.ParseInt(q.Get("device"), 10, 64)
	kind := q.Get("kind")
	rng := ranges[q.Get("range")]
	if rng == 0 {
		rng = 6 * time.Hour
	}
	until := time.Now()
	since := until.Add(-rng)

	type resp struct {
		TS     []int64               `json:"ts"`
		Series map[string][]*float64 `json:"series"`
	}
	align := func(metric string, ifindex int) ([]store.Sample, error) {
		return s.Store.Samples(devID, ifindex, metric, since, until)
	}
	out := resp{Series: map[string][]*float64{}}
	if kind == "bandwidth" {
		ifidx, _ := strconv.Atoi(q.Get("ifindex"))
		inS, err := align("if_in_bps", ifidx)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		outS, _ := align("if_out_bps", ifidx)
		// union timestamps
		tset := map[int64]bool{}
		inMap, outMap := map[int64]float64{}, map[int64]float64{}
		for _, p := range inS {
			tset[p.TS] = true
			inMap[p.TS] = p.Value
		}
		for _, p := range outS {
			tset[p.TS] = true
			outMap[p.TS] = p.Value
		}
		ts := make([]int64, 0, len(tset))
		for t := range tset {
			ts = append(ts, t)
		}
		// sort
		for i := 0; i < len(ts); i++ {
			for j := i + 1; j < len(ts); j++ {
				if ts[j] < ts[i] {
					ts[i], ts[j] = ts[j], ts[i]
				}
			}
		}
		out.TS = ts
		inArr := make([]*float64, len(ts))
		outArr := make([]*float64, len(ts))
		for i, t := range ts {
			if v, ok := inMap[t]; ok {
				vv := v
				inArr[i] = &vv
			}
			if v, ok := outMap[t]; ok {
				vv := v
				outArr[i] = &vv
			}
		}
		out.Series["in"] = inArr
		out.Series["out"] = outArr
	} else {
		pts, err := align(kind, -2)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		out.TS = make([]int64, len(pts))
		arr := make([]*float64, len(pts))
		for i, p := range pts {
			out.TS[i] = p.TS
			v := p.Value
			arr[i] = &v
		}
		out.Series["value"] = arr
	}
	if out.TS == nil {
		out.TS = []int64{}
	}
	writeJSON(w, 200, out)
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDR      string `json:"cidr"`
		Community string `json:"community"`
	}
	if !decode(w, r, &req) {
		return
	}
	found, err := discovery.Scan(req.CIDR, req.Community)
	if err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	if found == nil {
		found = []discovery.Found{}
	}
	writeJSON(w, 200, found)
}

// legacyConnReq is the body of the preview/import endpoints: either the
// manual connection fields (ConnConfig) or an auto-detected target
// (Direct Import), in which case the server re-derives the working
// connection itself — no credentials ever round-trip through the browser.
type legacyConnReq struct {
	legacyimport.ConnConfig
	AutoTarget *legacyimport.AutoTarget `json:"auto_target"`
}

func (s *Server) legacySource(w http.ResponseWriter, r *http.Request) (*legacyimport.SQLSource, bool) {
	var req legacyConnReq
	if !decode(w, r, &req) {
		return nil, false
	}
	cfg := req.ConnConfig
	if req.AutoTarget != nil {
		resolved, err := legacyimport.ResolveAutoConn(*req.AutoTarget)
		if err != nil {
			errJSON(w, 502, err.Error())
			return nil, false
		}
		cfg = resolved
	}
	src, err := legacyimport.Connect(cfg)
	if err != nil {
		errJSON(w, 502, err.Error())
		return nil, false
	}
	return src, true
}

func (s *Server) legacyDetect(w http.ResponseWriter, r *http.Request) {
	cands := legacyimport.Detect()
	if cands == nil {
		cands = []legacyimport.Candidate{}
	}
	writeJSON(w, 200, map[string]any{"candidates": cands})
}

// legacyImportAuto runs the Preview for an auto-detected target; the UI
// then offers the real import through POST /api/legacy/import with the
// same auto target.
func (s *Server) legacyImportAuto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoTarget *legacyimport.AutoTarget `json:"auto_target"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.AutoTarget == nil {
		errJSON(w, 400, "auto_target is required")
		return
	}
	cfg, err := legacyimport.ResolveAutoConn(*req.AutoTarget)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	src, err := legacyimport.Connect(cfg)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	defer src.Close()
	p, err := legacyimport.PreviewImport(src)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, p)
}

// writeBakError sends a structured .bak situation error: the precise
// message, its kind, and the per-candidate details for the UI.
func writeBakError(w http.ResponseWriter, e *legacyimport.BakImportError) {
	writeJSON(w, 502, e)
}

// writeBakClientError sends a 400 in the same structured shape as
// writeBakError (v2.2.1: EVERY failure of the .bak endpoint carries
// error_kind + candidates, so the Import page handles them all
// uniformly — previously the validation exits returned a bare
// {"error": ...} with neither field).
func writeBakClientError(w http.ResponseWriter, msg string) {
	writeJSON(w, 400, legacyimport.StructuredBakError(legacyimport.BakInvalidRequest, msg))
}

// legacyImportBak imports from a .bak backup file. The file is restored
// into a temporary database on a SQL Server, imported, and the
// temporary database is dropped again. The server is chosen in this
// order: the card's manual override (when a server was typed), the
// given auto target, or the first detected server that accepts a
// connection. When no server is usable, the response says precisely
// which situation applies (none found / found but not answering /
// answering but sign-in failed) instead of one generic message.
func (s *Server) legacyImportBak(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path       string                      `json:"path"`
		AutoTarget *legacyimport.AutoTarget    `json:"auto_target"`
		Manual     *legacyimport.BakManualConn `json:"manual"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Manual override: when a server was typed, it wins over
	// auto-detection. The backup path is then a path on the SQL Server
	// computer, so it is validated for form only, not for local
	// existence.
	if req.Manual != nil && strings.TrimSpace(req.Manual.Server) != "" {
		if err := legacyimport.ValidateBakPathRemote(req.Path); err != nil {
			writeBakClientError(w, err.Error())
			return
		}
		cfg, err := legacyimport.ManualServerConn(*req.Manual)
		if err != nil {
			writeBakClientError(w, err.Error())
			return
		}
		if berr := legacyimport.CheckManualServer(cfg); berr != nil {
			writeBakError(w, berr)
			return
		}
		rep, err := legacyimport.ImportBak(cfg, req.Path, s.Store)
		if err != nil {
			writeBakError(w, legacyimport.StructuredBakError(legacyimport.BakImportFailed, err.Error()))
			return
		}
		writeJSON(w, 200, rep)
		return
	}
	if err := legacyimport.ValidateBakPath(req.Path); err != nil {
		writeBakClientError(w, err.Error())
		return
	}
	cands := legacyimport.Detect()
	target := req.AutoTarget
	if target == nil {
		for i := range cands {
			if cands[i].CanConnect {
				t := cands[i].Target
				target = &t
				break
			}
		}
		if target == nil {
			writeBakError(w, legacyimport.ClassifyBakSituation(cands))
			return
		}
	}
	cfg, err := legacyimport.ResolveAutoServerConn(*target)
	if err != nil {
		// The server state may have changed since detection; classify
		// from a fresh detection rather than blaming the wrong cause.
		writeBakError(w, legacyimport.ClassifyBakSituation(legacyimport.Detect()))
		return
	}
	rep, err := legacyimport.ImportBak(cfg, req.Path, s.Store)
	if err != nil {
		writeBakError(w, legacyimport.StructuredBakError(legacyimport.BakImportFailed, err.Error()))
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) legacyPreview(w http.ResponseWriter, r *http.Request) {
	src, ok := s.legacySource(w, r)
	if !ok {
		return
	}
	defer src.Close()
	p, err := legacyimport.PreviewImport(src)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) legacyImport(w http.ResponseWriter, r *http.Request) {
	src, ok := s.legacySource(w, r)
	if !ok {
		return
	}
	defer src.Close()
	rep, err := legacyimport.RunImport(src, s.Store)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, rep)
}
