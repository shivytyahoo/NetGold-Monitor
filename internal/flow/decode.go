// Package flow implements NetGold's traffic collector: NetFlow v5,
// NetFlow v9 and IPFIX decoding, sFlow v5 decoding, minute-bucket
// aggregation into the store, and a small in-memory ring of the most
// recent flows for the live view. No raw flows are kept in the database —
// only 1-minute aggregates (see store.AddFlow).
package flow

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Record is one decoded flow (a single flow record, or one sampled
// packet for sFlow).
type Record struct {
	Exporter  string    `json:"exporter"`
	Src       string    `json:"src"`
	Dst       string    `json:"dst"`
	SrcPort   int       `json:"src_port"`
	DstPort   int       `json:"dst_port"`
	Proto     int       `json:"proto"`
	Bytes     uint64    `json:"bytes"`
	Packets   uint64    `json:"packets"`
	TS        time.Time `json:"ts"`
	ProtoName string    `json:"proto_name,omitempty"`
}

func ipStr(b []byte) string {
	if len(b) != 4 {
		return ""
	}
	return net.IPv4(b[0], b[1], b[2], b[3]).String()
}

func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

// ---- NetFlow v5 ----

// DecodeNetFlowV5 decodes a full NetFlow v5 export packet.
func DecodeNetFlowV5(pkt []byte, exporter string, now time.Time) ([]Record, error) {
	if len(pkt) < 24 {
		return nil, errors.New("netflow v5: packet too short")
	}
	if v := be16(pkt[0:2]); v != 5 {
		return nil, fmt.Errorf("netflow v5: version is %d", v)
	}
	count := int(be16(pkt[2:4]))
	var out []Record
	off := 24
	for i := 0; i < count; i++ {
		if off+48 > len(pkt) {
			return out, fmt.Errorf("netflow v5: truncated at record %d", i)
		}
		r := pkt[off : off+48]
		out = append(out, Record{
			Exporter: exporter,
			Src:      ipStr(r[0:4]),
			Dst:      ipStr(r[4:8]),
			Packets:  uint64(be32(r[16:20])),
			Bytes:    uint64(be32(r[20:24])),
			SrcPort:  int(be16(r[32:34])),
			DstPort:  int(be16(r[34:36])),
			Proto:    int(r[38]),
			TS:       now,
		})
		off += 48
	}
	return out, nil
}

// ---- NetFlow v9 / IPFIX (shared template machinery) ----

// Field numbers (IANA IPFIX information elements, shared by v9).
const (
	fInBytes    = 1
	fInPackets  = 2
	fProtocol   = 4
	fL4SrcPort  = 7
	fIPv4Src    = 8
	fL4DstPort  = 11
	fIPv4Dst    = 12
	fIPv6Src    = 27
	fIPv6Dst    = 28
	fInBytesAlt = 23 // some exporters use postOctetDeltaCount as well; v9 IN_BYTES is 1
)

type template struct {
	fields []tplField
	size   int // total record size in bytes; -1 if variable-length present
}

type tplField struct {
	typ    uint16
	length uint16
}

// TemplateCache holds flow templates per exporter+source+template ID.
// It is safe for concurrent use.
type TemplateCache struct {
	mu sync.Mutex
	m  map[string]*template
}

func NewTemplateCache() *TemplateCache { return &TemplateCache{m: map[string]*template{}} }

func (c *TemplateCache) put(exporter string, sourceID uint32, tid uint16, t *template) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[fmt.Sprintf("%s|%d|%d", exporter, sourceID, tid)] = t
}

func (c *TemplateCache) get(exporter string, sourceID uint32, tid uint16) *template {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[fmt.Sprintf("%s|%d|%d", exporter, sourceID, tid)]
}

// DecodeTemplatePacket decodes a NetFlow v9 (version 9) or IPFIX
// (version 10) packet, learning templates as they arrive. Data records
// whose template has not been seen yet are skipped (exporters resend
// templates periodically).
func (c *TemplateCache) DecodeTemplatePacket(pkt []byte, exporter string, now time.Time) ([]Record, error) {
	if len(pkt) < 2 {
		return nil, errors.New("flow: packet too short")
	}
	switch v := be16(pkt[0:2]); v {
	case 9:
		return c.decodeV9(pkt, exporter, now)
	case 10:
		return c.decodeIPFIX(pkt, exporter, now)
	default:
		return nil, fmt.Errorf("flow: unsupported version %d", v)
	}
}

func (c *TemplateCache) decodeV9(pkt []byte, exporter string, now time.Time) ([]Record, error) {
	if len(pkt) < 20 {
		return nil, errors.New("netflow v9: packet too short")
	}
	sourceID := be32(pkt[16:20])
	var out []Record
	off := 20
	for off+4 <= len(pkt) {
		setID := be16(pkt[off : off+2])
		setLen := int(be16(pkt[off+2 : off+4]))
		if setLen < 4 || off+setLen > len(pkt) {
			break
		}
		body := pkt[off+4 : off+setLen]
		switch {
		case setID == 0: // template flowset
			c.parseV9Templates(body, exporter, sourceID)
		case setID >= 256:
			out = append(out, c.decodeDataSet(body, exporter, sourceID, setID, now)...)
		}
		off += setLen
	}
	return out, nil
}

func (c *TemplateCache) parseV9Templates(body []byte, exporter string, sourceID uint32) {
	for len(body) >= 4 {
		tid := be16(body[0:2])
		n := int(be16(body[2:4]))
		body = body[4:]
		if tid < 256 || n == 0 {
			return
		}
		t := &template{}
		ok := true
		for i := 0; i < n; i++ {
			if len(body) < 4 {
				ok = false
				break
			}
			f := tplField{typ: be16(body[0:2]), length: be16(body[2:4])}
			body = body[4:]
			if f.length == 0xFFFF {
				t.size = -1
			} else if t.size >= 0 {
				t.size += int(f.length)
			}
			t.fields = append(t.fields, f)
		}
		if !ok {
			return
		}
		c.put(exporter, sourceID, tid, t)
		// Template sets are padded to a 4-byte boundary.
		for len(body) > 0 && len(body) < 4 {
			body = body[1:]
		}
	}
}

func (c *TemplateCache) decodeIPFIX(pkt []byte, exporter string, now time.Time) ([]Record, error) {
	if len(pkt) < 16 {
		return nil, errors.New("ipfix: packet too short")
	}
	msgLen := int(be16(pkt[2:4]))
	if msgLen > len(pkt) {
		msgLen = len(pkt)
	}
	domainID := be32(pkt[12:16])
	var out []Record
	off := 16
	for off+4 <= msgLen {
		setID := be16(pkt[off : off+2])
		setLen := int(be16(pkt[off+2 : off+4]))
		if setLen < 4 || off+setLen > msgLen {
			break
		}
		body := pkt[off+4 : off+setLen]
		switch {
		case setID == 2: // template set
			c.parseIPFIXTemplates(body, exporter, domainID)
		case setID >= 256:
			out = append(out, c.decodeDataSet(body, exporter, domainID, setID, now)...)
		}
		off += setLen
	}
	return out, nil
}

func (c *TemplateCache) parseIPFIXTemplates(body []byte, exporter string, domainID uint32) {
	for len(body) >= 4 {
		tid := be16(body[0:2])
		n := int(be16(body[2:4]))
		body = body[4:]
		if tid < 256 {
			return
		}
		t := &template{}
		ok := true
		for i := 0; i < n; i++ {
			if len(body) < 4 {
				ok = false
				break
			}
			f := tplField{typ: be16(body[0:2]), length: be16(body[2:4])}
			body = body[4:]
			if f.typ&0x8000 != 0 { // enterprise field: 4-byte enterprise number follows
				if len(body) < 4 {
					ok = false
					break
				}
				body = body[4:]
				f.typ &^= 0x8000
			}
			if f.length == 0xFFFF {
				t.size = -1
			} else if t.size >= 0 {
				t.size += int(f.length)
			}
			t.fields = append(t.fields, f)
		}
		if !ok {
			return
		}
		c.put(exporter, domainID, tid, t)
	}
}

// decodeDataSet decodes one data flowset using a cached template.
func (c *TemplateCache) decodeDataSet(body []byte, exporter string, sourceID uint32, tid uint16, now time.Time) []Record {
	t := c.get(exporter, sourceID, tid)
	if t == nil || t.size <= 0 {
		return nil
	}
	var out []Record
	for len(body) >= t.size {
		rec := body[:t.size]
		body = body[t.size:]
		r := Record{Exporter: exporter, TS: now, Packets: 1}
		pos := 0
		for _, f := range t.fields {
			if pos+int(f.length) > len(rec) {
				break
			}
			v := rec[pos : pos+int(f.length)]
			pos += int(f.length)
			switch f.typ {
			case fInBytes:
				r.Bytes = beUint(v)
			case fInPackets:
				r.Packets = beUint(v)
			case fProtocol:
				r.Proto = int(beUint(v))
			case fL4SrcPort:
				r.SrcPort = int(beUint(v))
			case fL4DstPort:
				r.DstPort = int(beUint(v))
			case fIPv4Src:
				r.Src = ipStr(v)
			case fIPv4Dst:
				r.Dst = ipStr(v)
			case fIPv6Src:
				if len(v) == 16 {
					r.Src = net.IP(v).String()
				}
			case fIPv6Dst:
				if len(v) == 16 {
					r.Dst = net.IP(v).String()
				}
			}
		}
		if r.Src != "" || r.Dst != "" {
			out = append(out, r)
		}
		// Data sets are padded to a 4-byte boundary at the end; a short
		// remainder (< one record) is padding and ends the loop.
	}
	return out
}

func beUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}
