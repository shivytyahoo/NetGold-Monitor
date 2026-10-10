package poller

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"netgold/internal/store"
)

// Service checks (v2.0) implement the executable monitors. The semantics
// follow the legacy system's seeded TCP monitor scripts (installer study,
// bcp_ActiveMonitorTypeData): generic TCP timeout 5 s, Send/Expect steps —
//
//	FTP 21:    expect banner starting "2", send QUIT
//	HTTP 80:   send "HEAD / HTTP/1.0" with "User-Agent: NetGold/2.0",
//	           expect a 2xx/3xx status line
//	HTTPS 443: same HEAD check over TLS
//	SMTP 25:   expect banner starting "2", send QUIT
//	POP3 110:  expect greeting starting "+OK", send QUIT
//	IMAP 143:  expect greeting containing "* OK", send LOGOUT
//	NNTP 119:  expect banner starting "2", send QUIT
//	Telnet 23: connect = up
//	Echo 7:    send "abcdefghijkl", expect the same string back
//	Time 37:   any response = up
//	DNS 53:    a real name-resolution query against the device
//	SSH 22:    connect and expect an "SSH" banner
//	anything else: TCP connect = up
//
// Check results are monitor-level only: they never change the device's
// own up/down status (that stays driven by ping/SNMP).

// DefaultServiceTimeout is the legacy system's generic TCP monitor timeout.
const DefaultServiceTimeout = 5 * time.Second

type CheckResult struct {
	Up      bool
	Latency time.Duration
	Detail  string
}

// checkKind derives the protocol from the monitor name (the legacy system
// monitor type names: FTP, HTTP, HTTPS, SMTP, POP3, IMAP4, NNTP, Telnet,
// DNS, Echo, Time, SSH, ...) with the port as a fallback.
func checkKind(m store.DeviceMonitor) string {
	n := strings.ToLower(m.Name)
	switch {
	case strings.Contains(n, "https"):
		return "https"
	case strings.Contains(n, "http"):
		return "http"
	case strings.Contains(n, "ftp"):
		return "ftp"
	case strings.Contains(n, "smtp"):
		return "smtp"
	case strings.Contains(n, "pop3"):
		return "pop3"
	case strings.Contains(n, "imap"):
		return "imap"
	case strings.Contains(n, "nntp"):
		return "nntp"
	case strings.Contains(n, "telnet"):
		return "telnet"
	case strings.Contains(n, "dns"):
		return "dns"
	case strings.Contains(n, "echo"):
		return "echo"
	case strings.Contains(n, "time") || strings.Contains(n, "ntp"):
		return "time"
	case strings.Contains(n, "ssh"):
		return "ssh"
	}
	switch m.Port {
	case 21:
		return "ftp"
	case 80:
		return "http"
	case 443:
		return "https"
	case 25:
		return "smtp"
	case 110:
		return "pop3"
	case 143:
		return "imap"
	case 119:
		return "nntp"
	case 53:
		return "dns"
	case 7:
		return "echo"
	case 37:
		return "time"
	case 22:
		return "ssh"
	}
	return "tcp"
}

func checkTimeout(m store.DeviceMonitor) time.Duration {
	if m.TimeoutSec > 0 {
		return time.Duration(m.TimeoutSec) * time.Second
	}
	return DefaultServiceTimeout
}

// RunServiceCheck executes one service monitor against a device IP.
func RunServiceCheck(m store.DeviceMonitor, ip string) CheckResult {
	kind := checkKind(m)
	timeout := checkTimeout(m)
	attempts := m.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var res CheckResult
	for i := 0; i < attempts; i++ {
		res = runCheckOnce(kind, m, ip, timeout)
		if res.Up {
			return res
		}
	}
	return res
}

func runCheckOnce(kind string, m store.DeviceMonitor, ip string, timeout time.Duration) CheckResult {
	start := time.Now()
	done := func(up bool, detail string) CheckResult {
		return CheckResult{Up: up, Latency: time.Since(start), Detail: detail}
	}
	if kind == "dns" {
		return checkDNS(m, ip, timeout, start)
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(m.Port))
	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if kind == "https" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{
			InsecureSkipVerify: true, // monitoring probe: self-signed device certs still prove the service answers
			ServerName:         ip,
		})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return done(false, "connect failed: "+shortErr(err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	r := bufio.NewReader(conn)
	send := func(s string) {
		_, _ = conn.Write([]byte(s))
	}
	readLine := func() (string, error) {
		return r.ReadString('\n')
	}

	switch kind {
	case "telnet", "tcp":
		return done(true, "tcp connect ok")
	case "ftp", "smtp", "nntp":
		banner, err := readLine()
		if err != nil {
			return done(false, "no banner: "+shortErr(err))
		}
		if !strings.HasPrefix(strings.TrimSpace(banner), "2") {
			return done(false, "unexpected banner: "+trimBanner(banner))
		}
		send("QUIT\r\n")
		return done(true, "banner ok: "+trimBanner(banner))
	case "pop3":
		banner, err := readLine()
		if err != nil {
			return done(false, "no greeting: "+shortErr(err))
		}
		if !strings.HasPrefix(strings.TrimSpace(banner), "+OK") {
			return done(false, "unexpected greeting: "+trimBanner(banner))
		}
		send("QUIT\r\n")
		return done(true, "greeting ok: "+trimBanner(banner))
	case "imap":
		banner, err := readLine()
		if err != nil {
			return done(false, "no greeting: "+shortErr(err))
		}
		if !strings.Contains(banner, "* OK") {
			return done(false, "unexpected greeting: "+trimBanner(banner))
		}
		send("a001 LOGOUT\r\n")
		return done(true, "greeting ok: "+trimBanner(banner))
	case "http", "https":
		path := "/"
		if strings.HasPrefix(m.Target, "/") {
			path = m.Target
		}
		send("HEAD " + path + " HTTP/1.0\r\nUser-Agent: NetGold/2.0\r\nHost: " + ip + "\r\n\r\n")
		line, err := readLine()
		if err != nil {
			return done(false, "no response: "+shortErr(err))
		}
		code := httpStatusCode(line)
		if code >= 200 && code < 400 {
			return done(true, "HTTP "+strconv.Itoa(code))
		}
		if code > 0 {
			return done(false, "HTTP "+strconv.Itoa(code))
		}
		return done(false, "unexpected response: "+trimBanner(line))
	case "echo":
		send("abcdefghijkl")
		buf := make([]byte, 12)
		n, err := r.Read(buf)
		if err != nil || n == 0 {
			return done(false, "no echo: "+shortErr(err))
		}
		if string(buf[:n]) != "abcdefghijkl"[:n] {
			return done(false, "unexpected echo data")
		}
		return done(true, "echo ok")
	case "time":
		buf := make([]byte, 4)
		n, err := r.Read(buf)
		if err != nil && n == 0 {
			return done(false, "no response: "+shortErr(err))
		}
		return done(true, "time service responded")
	case "ssh":
		banner, err := readLine()
		if err != nil {
			return done(false, "no banner: "+shortErr(err))
		}
		if !strings.HasPrefix(banner, "SSH") {
			return done(false, "unexpected banner: "+trimBanner(banner))
		}
		return done(true, "banner ok: "+trimBanner(banner))
	}
	return done(true, "tcp connect ok")
}

// checkDNS performs a real DNS resolution query against the device (the
// target name comes from the monitor's Target, default "google.com").
func checkDNS(m store.DeviceMonitor, ip string, timeout time.Duration, start time.Time) CheckResult {
	name := strings.TrimSpace(m.Target)
	if name == "" {
		name = "google.com"
	}
	server := net.JoinHostPort(ip, "53")
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "udp", server)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	addrs, err := resolver.LookupHost(ctx, name)
	if err != nil {
		return CheckResult{Up: false, Latency: time.Since(start), Detail: "DNS query for " + name + " failed: " + shortErr(err)}
	}
	return CheckResult{Up: true, Latency: time.Since(start),
		Detail: fmt.Sprintf("DNS resolved %s (%d address(es))", name, len(addrs))}
}

func httpStatusCode(statusLine string) int {
	parts := strings.Fields(statusLine)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return code
}

func trimBanner(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 90 {
		s = s[:90] + "..."
	}
	return s
}
