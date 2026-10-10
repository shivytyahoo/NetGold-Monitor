package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"netgold/internal/store"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.DB.Close() })
	return &Server{Store: st}
}

const testAdminPassword = "test-admin-password"

// authedHandler returns the server's handler plus a signed-in admin
// session cookie. Since v3.4 the whole portal sits behind sign-in,
// so tests sign in the same way the browser does: an admin password
// record in the settings store, then a real login POST.
func authedHandler(t *testing.T, s *Server) (http.Handler, *http.Cookie) {
	t.Helper()
	rec, err := hashPassword(testAdminPassword)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := s.Store.SetSetting(settingAdminPassword, rec); err != nil {
		t.Fatalf("SetSetting admin password: %v", err)
	}
	h := s.Handler()
	body, _ := json.Marshal(map[string]string{"id": "admin", "password": testAdminPassword})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body)))
	lrec := httptest.NewRecorder()
	h.ServeHTTP(lrec, req)
	if lrec.Code != http.StatusOK {
		t.Fatalf("test admin login: status %d; body: %s", lrec.Code, lrec.Body.String())
	}
	for _, c := range lrec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return h, c
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil, nil
}

func withCookie(req *http.Request, c *http.Cookie) *http.Request {
	req.AddCookie(c)
	return req
}

// Regression (v2.2.1): on a computer with no SQL Server at all, the
// .bak import must answer with the structured situation error —
// error_kind "no_instance" AND a non-null candidates array — because
// the Import page keys its guidance (and the automatic engine setup
// offer) off exactly that shape. This drives the real handler into
// the zero-candidates state; it relies on the test environment having
// no reachable SQL Server (as in the sandbox with SQL Server stopped,
// detection finds only the built-in default-port guess, which cannot
// connect, so the situation classifies as no_instance).
func TestImportBakNoInstanceStructuredError(t *testing.T) {
	s := testServer(t)
	h, cookie := authedHandler(t, s)
	bak := filepath.Join(t.TempDir(), "Legacy.bak")
	if err := os.WriteFile(bak, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"path": bak})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/legacy/import-bak", strings.NewReader(string(body))), cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response is not JSON: %v; body: %s", err, rec.Body.String())
	}
	if raw["error_kind"] != "no_instance" {
		t.Fatalf("error_kind = %v, want \"no_instance\"; body: %s", raw["error_kind"], rec.Body.String())
	}
	cands, present := raw["candidates"]
	if !present || cands == nil {
		t.Fatalf("candidates missing or null; body: %s", rec.Body.String())
	}
	arr, ok := cands.([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("candidates = %v, want a non-empty array", cands)
	}
	if msg, _ := raw["error"].(string); !strings.Contains(msg, "No SQL Server was found") {
		t.Fatalf("error message = %q, want the no-instance explanation", msg)
	}
}

// A .bak request with an invalid path must fail in the same
// structured shape (v2.2.1), not as a bare {"error": ...}.
func TestImportBakInvalidPathStructuredError(t *testing.T) {
	s := testServer(t)
	h, cookie := authedHandler(t, s)
	body, _ := json.Marshal(map[string]string{"path": "notes.txt"})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/legacy/import-bak", strings.NewReader(string(body))), cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response is not JSON: %v; body: %s", err, rec.Body.String())
	}
	if raw["error_kind"] != "invalid_request" {
		t.Fatalf("error_kind = %v, want \"invalid_request\"; body: %s", raw["error_kind"], rec.Body.String())
	}
	if cands, present := raw["candidates"]; !present || cands == nil {
		t.Fatalf("candidates missing or null; body: %s", rec.Body.String())
	}
}

// /api/version reports the exact build version (v2.2.2): the startup
// takeover probe and the page's stale-tab check both key off it.
func TestVersionEndpoint(t *testing.T) {
	s := testServer(t)
	h, cookie := authedHandler(t, s)
	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/version", nil), cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if body["version"] != Version {
		t.Fatalf("version = %q, want %q", body["version"], Version)
	}
}

// The served index page must carry this build's version (v2.2.2):
// the meta tag the page script reads, the sidebar footer and the
// About line are stamped at serve time from the same constant, and
// no placeholder token may leak through. The connection banner
// element must be present for the stale-tab / dead-server trap.
func TestIndexStampsBuildVersion(t *testing.T) {
	s := testServer(t)
	h, cookie := authedHandler(t, s)
	req := withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<meta name="netgold-build" content="` + Version + `">`,
		"NetGold Monitor v" + Version,
		"Version " + Version,
		`id="conn-banner"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("served index is missing %q", want)
		}
	}
	if strings.Contains(body, "__NETGOLD_VERSION__") {
		t.Fatal("served index still contains the version placeholder token")
	}
}

// The page script must contain both connection-trap code paths
// (v2.2.2): the lost-connection banner and the version-mismatch
// banner, both driven by /api/version.
func TestAppJSConnectionTrap(t *testing.T) {
	s := testServer(t)
	h, cookie := authedHandler(t, s)
	req := withCookie(httptest.NewRequest(http.MethodGet, "/app.js", nil), cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/api/version",
		"Connection to NetGold was lost",
		"Not connected",
		"is now running. Refresh the page (F5)",
		"checkServerVersion",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("served app.js is missing %q", want)
		}
	}
}

// The embedded UI assets must be served uncacheable (v2.2.1): the
// single-page app IS the product logic, and an upgraded program must
// never be paired with a stale cached copy of it.
func TestAssetsServedNoStore(t *testing.T) {
	s := testServer(t)
	h, cookie := authedHandler(t, s)
	for _, p := range []string{"/", "/app.js", "/style.css"} {
		req := withCookie(httptest.NewRequest(http.MethodGet, p, nil), cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", p, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("GET %s: Cache-Control = %q, want \"no-store\"", p, cc)
		}
	}
}
