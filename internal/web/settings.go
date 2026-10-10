package web

import (
	"fmt"
	"net/http"
	"time"

	"netgold/internal/notify"
)

// emailSettingsJSON is the API view of the email settings: the password
// is never returned — only whether one is stored.
func emailSettingsJSON(s notify.Settings) map[string]any {
	return map[string]any{
		"enabled":      s.Enabled,
		"smtp_host":    s.Host,
		"smtp_port":    s.Port,
		"tls_mode":     s.TLSMode,
		"username":     s.Username,
		"has_password": s.Password != "",
		"from":         s.From,
		"to":           s.To,
		"on_down":      s.OnDown,
		"on_up":        s.OnUp,
	}
}

func (s *Server) getEmailSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, emailSettingsJSON(notify.Load(s.Store)))
}

type emailSettingsReq struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"smtp_host"`
	Port     int    `json:"smtp_port"`
	TLSMode  string `json:"tls_mode"`
	Username string `json:"username"`
	Password string `json:"password"` // maskSecret = keep stored; "" = clear
	From     string `json:"from"`
	To       string `json:"to"`
	OnDown   bool   `json:"on_down"`
	OnUp     bool   `json:"on_up"`
}

func (req emailSettingsReq) toSettings(existing notify.Settings) notify.Settings {
	out := notify.Settings{
		Enabled: req.Enabled, Host: req.Host, Port: req.Port, TLSMode: req.TLSMode,
		Username: req.Username, From: req.From, To: req.To,
		OnDown: req.OnDown, OnUp: req.OnUp,
	}
	switch req.Password {
	case maskSecret:
		out.Password = existing.Password
	default:
		out.Password = req.Password
	}
	return out
}

func (s *Server) putEmailSettings(w http.ResponseWriter, r *http.Request) {
	var req emailSettingsReq
	if !decode(w, r, &req) {
		return
	}
	cfg := req.toSettings(notify.Load(s.Store))
	if cfg.Enabled {
		if err := notify.Validate(cfg); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
	}
	if err := notify.Save(s.Store, cfg); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, emailSettingsJSON(notify.Load(s.Store)))
}

// testEmail sends one test message with the saved settings.
func (s *Server) testEmail(w http.ResponseWriter, r *http.Request) {
	cfg := notify.Load(s.Store)
	if err := notify.Validate(cfg); err != nil {
		errJSON(w, 400, "Email settings are incomplete: "+err.Error())
		return
	}
	test := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: [NetGold] Test email\r\nDate: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nThis is a test email from NetGold Monitor.\r\nIf you received it, your email alert settings are working.\r\n",
		cfg.From, cfg.To, time.Now().Format(time.RFC1123Z)))
	if err := notify.Send(cfg, test); err != nil {
		errJSON(w, 502, "Sending the test email failed: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
