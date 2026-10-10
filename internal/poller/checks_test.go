package poller

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"netgold/internal/store"
)

// startTCPServer starts a throwaway TCP server on 127.0.0.1; handle runs
// once per accepted connection.
func startTCPServer(t *testing.T, handle func(net.Conn)) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestFTPCheckExpectBanner(t *testing.T) {
	ip, port := startTCPServer(t, func(c net.Conn) {
		defer c.Close()
		fmt.Fprintf(c, "220 Test FTP ready\r\n")
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		if !strings.HasPrefix(line, "QUIT") {
			t.Errorf("server expected QUIT, got %q", line)
		}
	})
	res := RunServiceCheck(store.DeviceMonitor{Name: "FTP", Port: port}, ip)
	if !res.Up {
		t.Fatalf("FTP check down: %s", res.Detail)
	}
}

func TestSMTPCheckRejectsBadBanner(t *testing.T) {
	ip, port := startTCPServer(t, func(c net.Conn) {
		defer c.Close()
		fmt.Fprintf(c, "554 Service unavailable\r\n")
		bufio.NewReader(c).ReadString('\n')
	})
	res := RunServiceCheck(store.DeviceMonitor{Name: "SMTP", Port: port}, ip)
	if res.Up {
		t.Fatalf("SMTP check should be down for a 554 banner, detail=%s", res.Detail)
	}
}

func TestPOP3AndIMAPGreetings(t *testing.T) {
	ip, port := startTCPServer(t, func(c net.Conn) {
		defer c.Close()
		fmt.Fprintf(c, "+OK POP3 ready\r\n")
		bufio.NewReader(c).ReadString('\n')
	})
	if res := RunServiceCheck(store.DeviceMonitor{Name: "POP3", Port: port}, ip); !res.Up {
		t.Fatalf("POP3 check down: %s", res.Detail)
	}
	ip2, port2 := startTCPServer(t, func(c net.Conn) {
		defer c.Close()
		fmt.Fprintf(c, "* OK IMAP ready\r\n")
		bufio.NewReader(c).ReadString('\n')
	})
	if res := RunServiceCheck(store.DeviceMonitor{Name: "IMAP4", Port: port2}, ip2); !res.Up {
		t.Fatalf("IMAP check down: %s", res.Detail)
	}
}

func TestHTTPCheckHeadAndStatus(t *testing.T) {
	var gotUA, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	res := RunServiceCheck(store.DeviceMonitor{Name: "HTTP", Port: port}, host)
	if !res.Up {
		t.Fatalf("HTTP check down: %s", res.Detail)
	}
	if gotMethod != "HEAD" {
		t.Errorf("method = %q, want HEAD (the legacy system style)", gotMethod)
	}
	if gotUA != "NetGold/2.0" {
		t.Errorf("User-Agent = %q, want NetGold/2.0", gotUA)
	}
}

func TestHTTPCheckServerErrorIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	res := RunServiceCheck(store.DeviceMonitor{Name: "HTTP", Port: port}, host)
	if res.Up {
		t.Fatalf("HTTP 500 should be down, detail=%s", res.Detail)
	}
}

func TestTelnetConnectOnlyAndRefused(t *testing.T) {
	ip, port := startTCPServer(t, func(c net.Conn) { c.Close() })
	if res := RunServiceCheck(store.DeviceMonitor{Name: "Telnet", Port: port}, ip); !res.Up {
		t.Fatalf("telnet connect check down: %s", res.Detail)
	}
	// Nothing listening on this port: must be down.
	res := RunServiceCheck(store.DeviceMonitor{Name: "Telnet", Port: port + 1000}, ip)
	if res.Up {
		t.Fatal("check against a closed port should be down")
	}
}

func TestCheckKindMapping(t *testing.T) {
	cases := []struct {
		m    store.DeviceMonitor
		want string
	}{
		{store.DeviceMonitor{Name: "HTTPS"}, "https"},
		{store.DeviceMonitor{Name: "HTTP"}, "http"},
		{store.DeviceMonitor{Name: "FTP", Port: 21}, "ftp"},
		{store.DeviceMonitor{Name: "Custom", Port: 25}, "smtp"},
		{store.DeviceMonitor{Name: "Custom", Port: 9999}, "tcp"},
		{store.DeviceMonitor{Name: "DNS", Port: 53}, "dns"},
	}
	for _, c := range cases {
		if got := checkKind(c.m); got != c.want {
			t.Errorf("checkKind(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}
