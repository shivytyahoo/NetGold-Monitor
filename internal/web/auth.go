package web

// Sign-in and roles (v3.4). NetGold's portal is used from other
// computers on the LAN, so the whole interface sits behind a login
// page. There are exactly two roles:
//
//   - admin  — can view and modify everything, and is the only
//     role that can create or change sign-in credentials.
//   - viewer — read-only: every page, report download and timeline
//     works, but no endpoint that changes state accepts a viewer
//     session. There is ONE shared viewer ID + password for all
//     viewers, set by the admin in Settings → User Access.
//
// Passwords are stored as PBKDF2-HMAC-SHA256 records (per-credential
// random salt) in the settings store — never in plain text, and the
// records are never returned by any API. Sessions are random tokens
// in an HttpOnly cookie, held server-side with a 30-day sliding
// expiry. Enforcement is centralised in authGuard: pages and API
// calls alike require a session, and every state-changing method
// requires the admin role.

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	roleAdmin  = "admin"
	roleViewer = "viewer"

	adminUserID = "admin"

	settingAdminPassword  = "auth.admin_password"
	settingViewerID       = "auth.viewer_id"
	settingViewerPassword = "auth.viewer_password"

	sessionCookieName = "netgold_session"
	sessionLifetime   = 30 * 24 * time.Hour

	pbkdf2Iterations = 210000
	pbkdf2SaltLen    = 16
	pbkdf2KeyLen     = 32

	loginMaxFailures  = 5
	loginLockDuration = 30 * time.Second
	loginFailWindow   = 10 * time.Minute
)

// ---- password records -------------------------------------------------

func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(key)), nil
}

func checkPassword(record, password string) bool {
	parts := strings.Split(record, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---- sessions ----------------------------------------------------------

type authSession struct {
	UserID  string
	Role    string
	Expires time.Time
}

type loginThrottle struct {
	Failures    int
	WindowStart time.Time
	LockedUntil time.Time
}

type ctxSessionKey struct{}

// authState lazily initialises the server's session and throttle
// maps (the Server value is also built directly in tests).
func (s *Server) authState() {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]*authSession{}
	}
	if s.throttles == nil {
		s.throttles = map[string]*loginThrottle{}
	}
}

func (s *Server) sessionFromRequest(r *http.Request) (string, *authSession) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", nil
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	sess, ok := s.sessions[c.Value]
	if !ok {
		return "", nil
	}
	if time.Now().After(sess.Expires) {
		delete(s.sessions, c.Value)
		return "", nil
	}
	return c.Value, sess
}

// issueSession creates a session for the signed-in user and sets
// the cookie on the response.
func (s *Server) issueSession(w http.ResponseWriter, userID, role string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		errJSON(w, 500, "could not create a session")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.authMu.Lock()
	s.sessions[token] = &authSession{UserID: userID, Role: role, Expires: time.Now().Add(sessionLifetime)}
	s.authMu.Unlock()
	setSessionCookie(w, token)
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime / time.Second),
		Expires:  time.Now().Add(sessionLifetime),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func sessionOf(r *http.Request) *authSession {
	sess, _ := r.Context().Value(ctxSessionKey{}).(*authSession)
	return sess
}

// ---- stored credentials -------------------------------------------------

func (s *Server) adminConfigured() bool {
	_, ok := s.Store.GetSetting(settingAdminPassword)
	return ok
}

func (s *Server) viewerCredentials() (id string, configured bool) {
	id, _ = s.Store.GetSetting(settingViewerID)
	_, hasPw := s.Store.GetSetting(settingViewerPassword)
	return id, id != "" && hasPw
}

// ---- login throttling ----------------------------------------------------

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) loginLocked(ip string) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	t, ok := s.throttles[ip]
	return ok && time.Now().Before(t.LockedUntil)
}

func (s *Server) loginFailed(ip string) {
	now := time.Now()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	t, ok := s.throttles[ip]
	if !ok || now.Sub(t.WindowStart) > loginFailWindow {
		t = &loginThrottle{WindowStart: now}
		s.throttles[ip] = t
	}
	t.Failures++
	if t.Failures >= loginMaxFailures {
		t.LockedUntil = now.Add(loginLockDuration)
		t.Failures = 0
		t.WindowStart = now
	}
}

func (s *Server) loginSucceeded(ip string) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	delete(s.throttles, ip)
}

// ---- handlers -------------------------------------------------------------

var loginPage []byte

func (s *Server) serveLoginPage(w http.ResponseWriter, r *http.Request) {
	if loginPage == nil {
		raw, err := assets.ReadFile("assets/login.html")
		if err != nil {
			http.Error(w, "login page unavailable", http.StatusInternalServerError)
			return
		}
		loginPage = bytes.ReplaceAll(raw, []byte("__NETGOLD_VERSION__"), []byte(Version))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(loginPage)
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	_, viewerOK := s.viewerCredentials()
	writeJSON(w, 200, map[string]any{
		"admin_configured":  s.adminConfigured(),
		"viewer_configured": viewerOK,
	})
}

func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	if s.adminConfigured() {
		errJSON(w, 409, "The administrator password has already been created.")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Password) < 6 {
		errJSON(w, 400, "Choose a password of at least 6 characters.")
		return
	}
	rec, err := hashPassword(req.Password)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if err := s.Store.SetSetting(settingAdminPassword, rec); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	s.issueSession(w, adminUserID, roleAdmin)
	writeJSON(w, 200, map[string]string{"id": adminUserID, "role": roleAdmin})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.loginLocked(ip) {
		errJSON(w, 429, "Too many failed sign-in attempts. Wait a few seconds and try again.")
		return
	}
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	role := ""
	if req.ID == adminUserID {
		if rec, ok := s.Store.GetSetting(settingAdminPassword); ok && checkPassword(rec, req.Password) {
			role = roleAdmin
		}
	} else if vid, configured := s.viewerCredentials(); configured && req.ID == vid {
		if rec, ok := s.Store.GetSetting(settingViewerPassword); ok && checkPassword(rec, req.Password) {
			role = roleViewer
		}
	}
	if role == "" {
		s.loginFailed(ip)
		errJSON(w, 401, "Incorrect sign-in ID or password.")
		return
	}
	s.loginSucceeded(ip)
	s.issueSession(w, req.ID, role)
	writeJSON(w, 200, map[string]string{"id": req.ID, "role": role})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if token, _ := s.sessionFromRequest(r); token != "" {
		s.authMu.Lock()
		delete(s.sessions, token)
		s.authMu.Unlock()
	}
	clearSessionCookie(w)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionOf(r)
	if sess == nil {
		errJSON(w, 401, "Sign-in required.")
		return
	}
	writeJSON(w, 200, map[string]string{"id": sess.UserID, "role": sess.Role})
}

// requireAdmin is the second, explicit gate on the credential
// management endpoints: even a direct URL hit with a viewer session
// stops here (the middleware already blocks the writes; this also
// covers the read).
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	sess := sessionOf(r)
	if sess == nil {
		errJSON(w, 401, "Sign-in required.")
		return false
	}
	if sess.Role != roleAdmin {
		errJSON(w, 403, "Only an administrator can manage sign-in credentials.")
		return false
	}
	return true
}

func (s *Server) getAccessSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, configured := s.viewerCredentials()
	writeJSON(w, 200, map[string]any{"viewer_id": id, "viewer_configured": configured})
}

func (s *Server) putAccessSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req struct {
		ViewerID       string `json:"viewer_id"`
		ViewerPassword string `json:"viewer_password"` // empty = keep stored
	}
	if !decode(w, r, &req) {
		return
	}
	req.ViewerID = strings.TrimSpace(req.ViewerID)
	if req.ViewerID == adminUserID {
		errJSON(w, 400, "The viewer ID cannot be \"admin\" — that ID belongs to the administrator.")
		return
	}
	if req.ViewerID == "" {
		// Clearing the viewer ID disables viewer sign-in entirely.
		_ = s.Store.SetSetting(settingViewerID, "")
		_ = s.Store.SetSetting(settingViewerPassword, "")
		s.getAccessSettings(w, r)
		return
	}
	if req.ViewerPassword != "" {
		if len(req.ViewerPassword) < 6 {
			errJSON(w, 400, "Choose a viewer password of at least 6 characters.")
			return
		}
		rec, err := hashPassword(req.ViewerPassword)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if err := s.Store.SetSetting(settingViewerPassword, rec); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
	} else if _, configured := s.viewerCredentials(); !configured {
		errJSON(w, 400, "Set a viewer password to enable viewer sign-in.")
		return
	}
	if err := s.Store.SetSetting(settingViewerID, req.ViewerID); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	s.getAccessSettings(w, r)
}

func (s *Server) putAdminPassword(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	rec, ok := s.Store.GetSetting(settingAdminPassword)
	if !ok || !checkPassword(rec, req.CurrentPassword) {
		errJSON(w, 401, "The current administrator password is incorrect.")
		return
	}
	if len(req.NewPassword) < 6 {
		errJSON(w, 400, "Choose a password of at least 6 characters.")
		return
	}
	newRec, err := hashPassword(req.NewPassword)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if err := s.Store.SetSetting(settingAdminPassword, newRec); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- the central gate ------------------------------------------------------

// publicPath reports whether a request may proceed without a
// session: the login page itself, the sign-in endpoints it posts
// to, the status probe it reads, and the favicon files it names.
func publicPath(r *http.Request) bool {
	switch r.URL.Path {
	case "/login":
		return r.Method == http.MethodGet
	case "/api/auth/login", "/api/auth/setup":
		return r.Method == http.MethodPost
	case "/api/auth/status":
		return r.Method == http.MethodGet
	case "/favicon.ico", "/favicon-16.png", "/favicon-32.png", "/apple-touch-icon.png":
		return r.Method == http.MethodGet
	}
	return false
}

// readOnlyPost lists the POST endpoints a viewer session may use:
// signing out, and the tidy-layout computation — a pure function of
// its request body that stores nothing (the tidy view must work for
// read-only users too; it changes no data).
var readOnlyPosts = map[string]bool{
	"/api/auth/logout": true,
	"/api/tidy":        true,
}

func mutatingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// authGuard wraps the whole handler: no session, no portal — API
// calls get 401, page loads are redirected to the login page — and
// a viewer session cannot change anything anywhere.
func (s *Server) authGuard(next http.Handler) http.Handler {
	s.authState()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		token, sess := s.sessionFromRequest(r)
		if sess == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				errJSON(w, 401, "Sign-in required.")
			} else {
				http.Redirect(w, r, "/login", http.StatusFound)
			}
			return
		}
		if sess.Role == roleViewer && mutatingMethod(r.Method) && !readOnlyPosts[r.URL.Path] {
			errJSON(w, 403, "Read-only access: this change needs an administrator sign-in.")
			return
		}
		// Sliding expiry: a session used at least a day into its
		// 30 days is renewed for a fresh 30 days.
		if time.Until(sess.Expires) < sessionLifetime-24*time.Hour {
			s.authMu.Lock()
			sess.Expires = time.Now().Add(sessionLifetime)
			s.authMu.Unlock()
			setSessionCookie(w, token)
		}
		ctx := context.WithValue(r.Context(), ctxSessionKey{}, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
