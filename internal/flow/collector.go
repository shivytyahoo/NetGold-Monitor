package flow

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"netgold/internal/store"
)

// ProtocolNames maps IP protocol numbers to display names.
var ProtocolNames = map[int]string{
	1: "ICMP", 6: "TCP", 17: "UDP", 47: "GRE", 50: "ESP", 51: "AH",
	58: "ICMPv6", 89: "OSPF", 132: "SCTP",
}

// ProtoName returns the display name for an IP protocol number.
func ProtoName(n int) string {
	if s, ok := ProtocolNames[n]; ok {
		return s
	}
	return fmt.Sprintf("IP/%d", n)
}

const recentCap = 500

// Collector runs the UDP listeners and folds decoded flows into the
// store's minute buckets. Port 0 for a protocol disables its listener.
type Collector struct {
	Store       *store.Store
	NetFlowPort int
	SFlowPort   int

	mu       sync.Mutex
	recent   []Record // newest last
	packets  uint64
	records  uint64
	badPkts  uint64
	counters uint64 // sFlow counter samples seen (contents not decoded)

	templates *TemplateCache
	listeners []string
}

func NewCollector(st *store.Store, netflowPort, sflowPort int) *Collector {
	return &Collector{Store: st, NetFlowPort: netflowPort, SFlowPort: sflowPort,
		templates: NewTemplateCache()}
}

// Start launches the enabled listeners; it returns after binding so a
// port clash is reported at startup. Listeners stop when ctx ends.
func (c *Collector) Start(ctx context.Context) error {
	if c.NetFlowPort > 0 {
		conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", c.NetFlowPort))
		if err != nil {
			return fmt.Errorf("netflow listener: %w", err)
		}
		c.listeners = append(c.listeners, fmt.Sprintf("NetFlow/IPFIX UDP %d", c.NetFlowPort))
		go c.serve(ctx, conn, true)
	}
	if c.SFlowPort > 0 {
		conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", c.SFlowPort))
		if err != nil {
			return fmt.Errorf("sflow listener: %w", err)
		}
		c.listeners = append(c.listeners, fmt.Sprintf("sFlow UDP %d", c.SFlowPort))
		go c.serve(ctx, conn, false)
	}
	return nil
}

// Listeners describes the active listeners, for the API/status page.
func (c *Collector) Listeners() []string {
	out := make([]string, len(c.listeners))
	copy(out, c.listeners)
	return out
}

// Stats returns collector counters.
func (c *Collector) Stats() (packets, records, bad, counterSamples uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.packets, c.records, c.badPkts, c.counters
}

// Recent returns up to limit most recent flows, newest first.
func (c *Collector) Recent(limit int) []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit <= 0 || limit > len(c.recent) {
		limit = len(c.recent)
	}
	out := make([]Record, 0, limit)
	for i := len(c.recent) - 1; i >= 0 && len(out) < limit; i-- {
		r := c.recent[i]
		r.ProtoName = ProtoName(r.Proto)
		out = append(out, r)
	}
	return out
}

func (c *Collector) serve(ctx context.Context, conn net.PacketConn, isNetflow bool) {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return // closed
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		exporter := ""
		if ua, ok := addr.(*net.UDPAddr); ok {
			exporter = ua.IP.String()
		}
		c.handle(pkt, exporter, isNetflow)
	}
}

func (c *Collector) handle(pkt []byte, exporter string, isNetflow bool) {
	now := time.Now()
	var recs []Record
	var err error
	if isNetflow {
		if len(pkt) >= 2 && be16(pkt[0:2]) == 5 {
			recs, err = DecodeNetFlowV5(pkt, exporter, now)
		} else {
			recs, err = c.templates.DecodeTemplatePacket(pkt, exporter, now)
		}
	} else {
		var st *SFlowStats
		st, err = DecodeSFlow(pkt, exporter, now)
		if err == nil {
			recs = st.Records
			c.mu.Lock()
			c.counters += uint64(st.CounterSamples)
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	c.packets++
	if err != nil {
		c.badPkts++
		c.mu.Unlock()
		log.Printf("flow: undecodable packet from %s: %v", exporter, err)
		return
	}
	c.records += uint64(len(recs))
	c.mu.Unlock()
	bucket := now.Truncate(time.Minute).Unix()
	for _, r := range recs {
		if err := c.Store.AddFlow(bucket, r.Exporter, r.Src, r.Dst, r.DstPort, r.Proto, r.Bytes, r.Packets); err != nil {
			log.Printf("flow: store: %v", err)
		}
		c.mu.Lock()
		c.recent = append(c.recent, r)
		if len(c.recent) > recentCap {
			c.recent = append([]Record{}, c.recent[len(c.recent)-recentCap:]...)
		}
		c.mu.Unlock()
	}
}
