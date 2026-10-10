// Package notify sends email alerts on device up/down transitions.
//
// Configuration lives in NetGold's settings table (keys "email.*"). The
// SMTP password is stored only in the local database and is never
// rendered back in full by the web API (masked — see internal/web).
package notify

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"netgold/internal/store"
)

// Settings is the email alert configuration.
type Settings struct {
	Enabled  bool
	Host     string
	Port     int
	TLSMode  string // "none", "starttls", "tls"
	Username string
	Password string
	From     string
	To       string // comma-separated recipient list
	OnDown   bool
	OnUp     bool
}

const (
	KeyEnabled  = "email.enabled"
	KeyHost     = "email.smtp_host"
	KeyPort     = "email.smtp_port"
	KeyTLSMode  = "email.tls_mode"
	KeyUsername = "email.username"
	KeyPassword = "email.password"
	KeyFrom     = "email.from"
	KeyTo       = "email.to"
	KeyOnDown   = "email.on_down"
	KeyOnUp     = "email.on_up"
)

var allKeys = []string{KeyEnabled, KeyHost, KeyPort, KeyTLSMode, KeyUsername,
	KeyPassword, KeyFrom, KeyTo, KeyOnDown, KeyOnUp}

// Load reads the email settings from the store.
func Load(st *store.Store) Settings {
	m, _ := st.GetSettings(allKeys...)
	s := Settings{
		Enabled:  m[KeyEnabled] == "1",
		Host:     m[KeyHost],
		TLSMode:  m[KeyTLSMode],
		Username: m[KeyUsername],
		Password: m[KeyPassword],
		From:     m[KeyFrom],
		To:       m[KeyTo],
		OnDown:   m[KeyOnDown] != "0", // default on
		OnUp:     m[KeyOnUp] == "1",
	}
	if s.TLSMode == "" {
		s.TLSMode = "starttls"
	}
	s.Port, _ = strconv.Atoi(m[KeyPort])
	if s.Port == 0 {
		s.Port = 587
	}
	return s
}

// Save persists the email settings.
func Save(st *store.Store, s Settings) error {
	b2s := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	kv := map[string]string{
		KeyEnabled: b2s(s.Enabled), KeyHost: s.Host, KeyPort: strconv.Itoa(s.Port),
		KeyTLSMode: s.TLSMode, KeyUsername: s.Username, KeyPassword: s.Password,
		KeyFrom: s.From, KeyTo: s.To, KeyOnDown: b2s(s.OnDown), KeyOnUp: b2s(s.OnUp),
	}
	for k, v := range kv {
		if err := st.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

// Recipients parses the comma-separated To list.
func (s Settings) Recipients() []string {
	var out []string
	for _, p := range strings.Split(s.To, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks the settings before they are saved or used.
func Validate(s Settings) error {
	if s.Host == "" {
		return errors.New("SMTP host is required")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("SMTP port %d is out of range", s.Port)
	}
	switch s.TLSMode {
	case "none", "starttls", "tls":
	default:
		return fmt.Errorf("TLS mode %q is not supported (use none, starttls or tls)", s.TLSMode)
	}
	if s.From == "" || !strings.Contains(s.From, "@") {
		return errors.New("a valid From address is required")
	}
	rcpts := s.Recipients()
	if len(rcpts) == 0 {
		return errors.New("at least one recipient is required")
	}
	for _, r := range rcpts {
		if !strings.Contains(r, "@") {
			return fmt.Errorf("recipient %q does not look like an email address", r)
		}
	}
	return nil
}

// BuildMessage builds the full RFC 5322 message for a transition alert.
func BuildMessage(s Settings, d *store.Device, newStatus string, downtime time.Duration, now time.Time) []byte {
	var subject, headline string
	if newStatus == "down" {
		subject = fmt.Sprintf("[NetGold] DOWN: %s (%s)", d.Name, d.IP)
		headline = "is DOWN"
	} else {
		subject = fmt.Sprintf("[NetGold] UP: %s (%s)", d.Name, d.IP)
		headline = "is back UP"
	}
	var b strings.Builder
	b.WriteString("From: " + s.From + "\r\n")
	b.WriteString("To: " + strings.Join(s.Recipients(), ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "NetGold Monitor alert\n\nDevice %s (%s) %s.\n", d.Name, d.IP, headline)
	fmt.Fprintf(&b, "Time: %s\n", now.Format("2006-01-02 15:04:05 MST"))
	if d.Type != "" {
		fmt.Fprintf(&b, "Type: %s\n", d.Type)
	}
	if newStatus == "up" && downtime > 0 {
		fmt.Fprintf(&b, "Downtime: %s\n", formatDuration(downtime))
	}
	b.WriteString("\nThis message was sent by NetGold Monitor.\n")
	return []byte(b.String())
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// Send delivers one message using the configured SMTP server.
func Send(s Settings, msg []byte) error {
	if err := Validate(s); err != nil {
		return err
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var c *smtp.Client
	var err error
	switch s.TLSMode {
	case "tls":
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, tlsCfg)
		if derr != nil {
			return fmt.Errorf("connect (TLS): %w", derr)
		}
		c, err = smtp.NewClient(conn, s.Host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
	default:
		c, err = smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		if s.TLSMode == "starttls" {
			if ok, _ := c.Extension("STARTTLS"); ok {
				if err := c.StartTLS(tlsCfg); err != nil {
					c.Close()
					return fmt.Errorf("starttls: %w", err)
				}
			}
		}
	}
	defer c.Close()
	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range s.Recipients() {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return c.Quit()
}

// Notifier implements poller.TransitionNotifier on top of the settings
// stored in the database. Send failures are swallowed on purpose (the
// caller runs in a goroutine; a broken mail server must never disturb
// monitoring) — but they are returned by Send for the Settings "test"
// path and for unit tests.
type Notifier struct {
	Store *store.Store
	// SendFunc is injectable for tests; nil uses Send.
	SendFunc func(s Settings, msg []byte) error
}

func (n *Notifier) DeviceTransition(d *store.Device, newStatus string, downtime time.Duration) {
	s := Load(n.Store)
	if !s.Enabled {
		return
	}
	if newStatus == "down" && !s.OnDown {
		return
	}
	if newStatus == "up" && !s.OnUp {
		return
	}
	msg := BuildMessage(s, d, newStatus, downtime, time.Now())
	send := n.SendFunc
	if send == nil {
		send = Send
	}
	_ = send(s, msg)
}
