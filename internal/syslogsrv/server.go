package syslogsrv

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"netgold/internal/store"
)

// Server receives syslog on UDP and TCP and stores every message.
type Server struct {
	Store *store.Store
	Port  int // 0 disables the receiver

	received atomic.Uint64
	bad      atomic.Uint64 // parsed only as raw text (no PRI/header matched)
}

func (s *Server) Received() uint64 { return s.received.Load() }

// Start binds the UDP and TCP listeners; it returns after binding so a
// port clash is reported at startup. Listeners stop when ctx ends.
func (s *Server) Start(ctx context.Context) error {
	if s.Port <= 0 {
		return nil
	}
	udp, err := net.ListenPacket("udp", fmt.Sprintf(":%d", s.Port))
	if err != nil {
		return fmt.Errorf("syslog UDP listener: %w", err)
	}
	tcp, err := net.Listen("tcp", fmt.Sprintf(":%d", s.Port))
	if err != nil {
		udp.Close()
		return fmt.Errorf("syslog TCP listener: %w", err)
	}
	go func() {
		<-ctx.Done()
		udp.Close()
		tcp.Close()
	}()
	go s.serveUDP(udp)
	go s.serveTCP(ctx, tcp)
	return nil
}

func (s *Server) store(raw, sourceIP string) {
	e := Parse(raw, sourceIP, time.Now())
	s.received.Add(1)
	if err := s.Store.AddSyslog(e); err != nil {
		log.Printf("syslog: store: %v", err)
	}
}

func (s *Server) serveUDP(conn net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		src := ""
		if ua, ok := addr.(*net.UDPAddr); ok {
			src = ua.IP.String()
		}
		s.store(strings.TrimSpace(string(buf[:n])), src)
	}
}

func (s *Server) serveTCP(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleTCP(ctx, conn)
	}
}

// Syslog over TCP: RFC 6587 octet-counted framing when the line starts
// with a digit count, newline-delimited otherwise.
func (s *Server) handleTCP(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	src := ""
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		src = ta.IP.String()
	}
	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			msg := line
			// Octet counting: "<len> <message without trailing newline>".
			if n := octetCount(line); n > 0 {
				rest := line[len(fmt.Sprint(n))+1:]
				for len(rest) < n {
					more, err := br.ReadString('\n')
					if err != nil {
						break
					}
					rest += more
				}
				if len(rest) >= n {
					msg = rest[:n]
				}
			}
			s.store(msg, src)
		}
		if err != nil {
			return
		}
	}
}

// octetCount returns the leading octet count of an RFC 6587 frame, or 0
// when the line does not start with "<digits> ".
func octetCount(line string) int {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(line) || line[i] != ' ' {
		return 0
	}
	n := 0
	for _, c := range line[:i] {
		n = n*10 + int(c-'0')
	}
	if n <= 0 || n > 65535 {
		return 0
	}
	return n
}
