package web

// Sign-in and roles (v3.4): the portal is gated by a login page,
// admins can change things, viewers can only look. These tests drive
// the real HTTP handler end to end — setup, login, role enforcement,
// logout — because the middleware IS the security boundary.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doJSON(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func loginAs(t *testing.T, h http.Handler, id, password string) (*http.Cookie, int) {
	t.Helper()
	rec := doJSON(t, h, http.MethodPost, "/api/auth/login", map[string]string{"id": id, "password": password}, nil)
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c, rec.Code
		}
	}
	t.Fatalf("login as %q returned 200 without a session cookie", id)
	return nil, rec.Code
}

// setupAdmin provisions the admin password record directly (the
// first-run HTTP flow has its own test) and returns a handler.
func setupAdmin(t *testing.T, s *Server) http.Handler {
	t.Helper()
	rec, err := hashPassword(testAdminPassword)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := s.Store.SetSetting(settingAdminPassword, rec); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	return s.Handler()
}

func TestNoSessionIsLockedOut(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)

	// API calls get 401.
	if rec := doJSON(t, h, http.MethodGet, "/api/devices", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/devices without session = %d, want 401", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/version", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/version without session = %d, want 401", rec.Code)
	}
	// Page loads redirect to the login page.
	rec := doJSON(t, h, http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET / without session = %d (Location %q), want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
	// The login page itself is public and carries the branding.
	rec = doJSON(t, h, http.MethodGet, "/login", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "NetGold Monitor v"+Version) {
		t.Fatalf("GET /login = %d; body has branding: %v", rec.Code, strings.Contains(rec.Body.String(), "Shivam Bhalla"))
	}
	// A forged cookie is worth nothing.
	bogus := &http.Cookie{Name: sessionCookieName, Value: "not-a-real-token"}
	if rec := doJSON(t, h, http.MethodGet, "/api/devices", nil, bogus); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/devices with forged cookie = %d, want 401", rec.Code)
	}
}

func TestFirstRunSetupFlow(t *testing.T) {
	s := testServer(t) // no admin password anywhere
	h := s.Handler()

	rec := doJSON(t, h, http.MethodGet, "/api/auth/status", nil, nil)
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status not JSON: %v", err)
	}
	if st["admin_configured"] != false || st["viewer_configured"] != false {
		t.Fatalf("fresh install status = %v, want both false", st)
	}
	// Too-short password rejected.
	if rec := doJSON(t, h, http.MethodPost, "/api/auth/setup", map[string]string{"password": "abc"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("short setup password = %d, want 400", rec.Code)
	}
	// Setup creates the password AND signs the admin in.
	rec = doJSON(t, h, http.MethodPost, "/api/auth/setup", map[string]string{"password": "first-admin-pass"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d; body: %s", rec.Code, rec.Body.String())
	}
	var admin *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			admin = c
		}
	}
	if admin == nil {
		t.Fatal("setup did not set a session cookie")
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/devices", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("setup session cannot read devices: %d", rec.Code)
	}
	// The setup offer never appears again: a second setup is refused
	// even while unauthenticated.
	if rec := doJSON(t, h, http.MethodPost, "/api/auth/setup", map[string]string{"password": "second-attempt"}, nil); rec.Code != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", rec.Code)
	}
	// Stored value is a PBKDF2 record, never the plain password.
	stored, ok := s.Store.GetSetting(settingAdminPassword)
	if !ok || !strings.HasPrefix(stored, "pbkdf2-sha256$") || strings.Contains(stored, "first-admin-pass") {
		t.Fatalf("stored admin credential is not a PBKDF2 record: %q", stored)
	}
}

func TestLoginWrongThenRight(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)

	if _, code := loginAs(t, h, "admin", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("wrong admin password = %d, want 401", code)
	}
	if _, code := loginAs(t, h, "nobody", testAdminPassword); code != http.StatusUnauthorized {
		t.Fatalf("unknown ID = %d, want 401", code)
	}
	admin, code := loginAs(t, h, "admin", testAdminPassword)
	if code != http.StatusOK || admin == nil {
		t.Fatalf("admin login = %d", code)
	}
	rec := doJSON(t, h, http.MethodGet, "/api/auth/me", nil, admin)
	var me map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || me["role"] != roleAdmin || me["id"] != "admin" {
		t.Fatalf("/api/auth/me = %v (err %v), want admin/admin", me, err)
	}
}

// The role matrix: a viewer can read everything and download
// reports, but every state-changing endpoint refuses with 403 —
// including the credential-management endpoints hit by direct URL.
func TestViewerRoleMatrix(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)
	admin, _ := loginAs(t, h, "admin", testAdminPassword)

	// Viewer login is disabled until the admin enables it.
	if _, code := loginAs(t, h, "noc-view", "viewer-secret"); code != http.StatusUnauthorized {
		t.Fatalf("viewer login before being enabled = %d, want 401", code)
	}
	// Admin enables the shared viewer credentials.
	rec := doJSON(t, h, http.MethodPut, "/api/settings/access",
		map[string]string{"viewer_id": "noc-view", "viewer_password": "viewer-secret"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin set viewer credentials = %d; body: %s", rec.Code, rec.Body.String())
	}
	viewer, code := loginAs(t, h, "noc-view", "viewer-secret")
	if code != http.StatusOK || viewer == nil {
		t.Fatalf("viewer login = %d", code)
	}
	if _, code := loginAs(t, h, "noc-view", "wrong-secret"); code != http.StatusUnauthorized {
		t.Fatalf("wrong viewer password = %d, want 401", code)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/auth/me", nil, viewer)
	var me map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me["role"] != roleViewer || me["id"] != "noc-view" {
		t.Fatalf("viewer /api/auth/me = %v, want viewer/noc-view", me)
	}

	// Reads work for the viewer.
	for _, path := range []string{"/api/devices", "/api/stats", "/api/maps", "/api/topologies", "/api/settings/email"} {
		if rec := doJSON(t, h, http.MethodGet, path, nil, viewer); rec.Code != http.StatusOK {
			t.Fatalf("viewer GET %s = %d, want 200", path, rec.Code)
		}
	}
	// Report downloads are GETs: the viewer keeps them.
	if rec := doJSON(t, h, http.MethodGet, "/api/reports/downtime.xlsx", nil, viewer); rec.Code != http.StatusOK {
		t.Fatalf("viewer report download = %d, want 200", rec.Code)
	}

	// Every mutation is refused with 403.
	mutations := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/devices", map[string]any{"name": "x", "ip": "10.0.0.1"}},
		{http.MethodPut, "/api/devices/1", map[string]any{"name": "x"}},
		{http.MethodDelete, "/api/devices/1", nil},
		{http.MethodPost, "/api/groups", map[string]any{"name": "x"}},
		{http.MethodPost, "/api/devices/1/monitors", map[string]any{"type": "ping"}},
		{http.MethodPost, "/api/devices/1/poll", nil},
		{http.MethodPut, "/api/settings/email", map[string]any{"host": "smtp.example.com"}},
		{http.MethodPost, "/api/settings/email/test", nil},
		{http.MethodPost, "/api/legacy/import", map[string]any{}},
		{http.MethodPost, "/api/legacy/import-auto", nil},
		{http.MethodPost, "/api/legacy/import-bak", map[string]any{"path": "x.bak"}},
		{http.MethodPost, "/api/topologies", map[string]any{"name": "x"}},
		{http.MethodPut, "/api/topologies/1", map[string]any{"name": "x"}},
		{http.MethodDelete, "/api/topologies/1", nil},
		{http.MethodPost, "/api/discover", map[string]any{"subnet": "10.0.0.0/30"}},
		{http.MethodPost, "/api/links/discover", map[string]any{}},
		{http.MethodPut, "/api/settings/access", map[string]string{"viewer_id": "hax", "viewer_password": "haxpass1"}},
		{http.MethodPut, "/api/settings/access/admin-password", map[string]string{"current_password": testAdminPassword, "new_password": "haxpass1"}},
	}
	for _, m := range mutations {
		if rec := doJSON(t, h, m.method, m.path, m.body, viewer); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", m.method, m.path, rec.Code)
		}
	}
	// Even READING the credential panel directly is refused.
	if rec := doJSON(t, h, http.MethodGet, "/api/settings/access", nil, viewer); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer GET /api/settings/access = %d, want 403", rec.Code)
	}
	// The tidy computation stores nothing: the viewer may use it.
	rec = doJSON(t, h, http.MethodPost, "/api/tidy", map[string]any{
		"points": []map[string]any{{"id": 1, "x": 0, "y": 0}, {"id": 2, "x": 1, "y": 1}},
		"min_dx": 60.0, "min_dy": 100.0,
	}, viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer POST /api/tidy = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminCanMutate(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)
	admin, _ := loginAs(t, h, "admin", testAdminPassword)

	if rec := doJSON(t, h, http.MethodPost, "/api/groups", map[string]any{"name": "Core"}, admin); rec.Code != http.StatusCreated {
		t.Fatalf("admin POST /api/groups = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/topologies", map[string]any{"name": "T1"}, admin); rec.Code != http.StatusCreated {
		t.Fatalf("admin POST /api/topologies = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminPasswordChangeFlow(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)
	admin, _ := loginAs(t, h, "admin", testAdminPassword)

	// Wrong current password refuses.
	if rec := doJSON(t, h, http.MethodPut, "/api/settings/access/admin-password",
		map[string]string{"current_password": "nope", "new_password": "brand-new-pass"}, admin); rec.Code != http.StatusUnauthorized {
		t.Fatalf("change with wrong current = %d, want 401", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/settings/access/admin-password",
		map[string]string{"current_password": testAdminPassword, "new_password": "brand-new-pass"}, admin); rec.Code != http.StatusOK {
		t.Fatalf("change admin password = %d; body: %s", rec.Code, rec.Body.String())
	}
	// Old password dead, new password works.
	if _, code := loginAs(t, h, "admin", testAdminPassword); code != http.StatusUnauthorized {
		t.Fatalf("old admin password after change = %d, want 401", code)
	}
	if _, code := loginAs(t, h, "admin", "brand-new-pass"); code != http.StatusOK {
		t.Fatalf("new admin password = %d, want 200", code)
	}
}

func TestLogoutEndsSession(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)
	admin, _ := loginAs(t, h, "admin", testAdminPassword)
	if rec := doJSON(t, h, http.MethodGet, "/api/devices", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("before logout GET = %d, want 200", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/auth/logout", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/devices", nil, admin); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout GET = %d, want 401", rec.Code)
	}
}

func TestLoginThrottleLocks(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)
	// Five failures from one source lock the form.
	for i := 0; i < loginMaxFailures; i++ {
		if _, code := loginAs(t, h, "admin", "bad-password"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i+1, code)
		}
	}
	// Even the CORRECT password is refused while locked.
	if _, code := loginAs(t, h, "admin", testAdminPassword); code != http.StatusTooManyRequests {
		t.Fatalf("login while locked = %d, want 429", code)
	}
}

func TestPasswordRecordRoundtrip(t *testing.T) {
	rec, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if strings.Contains(rec, "correct horse") {
		t.Fatal("record contains the plain password")
	}
	if !checkPassword(rec, "correct horse battery staple") {
		t.Fatal("checkPassword rejected the right password")
	}
	if checkPassword(rec, "wrong") {
		t.Fatal("checkPassword accepted a wrong password")
	}
	if checkPassword("garbage", "x") || checkPassword("", "x") {
		t.Fatal("checkPassword accepted a malformed record")
	}
	other, _ := hashPassword("correct horse battery staple")
	if other == rec {
		t.Fatal("two records for one password are identical — the salt is not random")
	}
}

// The access settings read never leaks credential material.
func TestAccessSettingsNeverLeakSecrets(t *testing.T) {
	s := testServer(t)
	h := setupAdmin(t, s)
	admin, _ := loginAs(t, h, "admin", testAdminPassword)
	doJSON(t, h, http.MethodPut, "/api/settings/access",
		map[string]string{"viewer_id": "noc-view", "viewer_password": "viewer-secret"}, admin)
	rec := doJSON(t, h, http.MethodGet, "/api/settings/access", nil, admin)
	body := rec.Body.String()
	if strings.Contains(body, "viewer-secret") || strings.Contains(body, "pbkdf2") {
		t.Fatalf("access settings response leaks credential material: %s", body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if out["viewer_id"] != "noc-view" || out["viewer_configured"] != true {
		t.Fatalf("access settings = %v, want noc-view/true", out)
	}
}
