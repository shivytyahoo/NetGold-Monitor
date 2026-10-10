package flow

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ---- sFlow v5 ----
//
// Datagrams are XDR-encoded (all integers big-endian, opaque data padded
// to a 4-byte boundary). We decode flow samples (format 1 and the
// expanded format 3) whose records carry a sampled packet header
// (record format 1), and count counter samples without decoding them.

type xdr struct {
	b []byte
	i int
}

func (x *xdr) u32() (uint32, bool) {
	if x.i+4 > len(x.b) {
		return 0, false
	}
	v := binary.BigEndian.Uint32(x.b[x.i:])
	x.i += 4
	return v, true
}

func (x *xdr) bytes(n int) ([]byte, bool) {
	if n < 0 || x.i+n > len(x.b) {
		return nil, false
	}
	v := x.b[x.i : x.i+n]
	x.i += n
	return v, true
}

// opaque reads a length-prefixed, 4-byte-padded opaque value.
func (x *xdr) opaque() ([]byte, bool) {
	n, ok := x.u32()
	if !ok {
		return nil, false
	}
	data, ok := x.bytes(int(n))
	if !ok {
		return nil, false
	}
	if pad := (4 - (n % 4)) % 4; pad > 0 {
		if _, ok := x.bytes(int(pad)); !ok {
			return nil, false
		}
	}
	return data, true
}

func splitTag(tag uint32) (enterprise, format uint32) {
	return tag >> 12, tag & 0xFFF
}

// SFlowStats summarises one decoded datagram.
type SFlowStats struct {
	FlowSamples    int
	CounterSamples int
	Records        []Record
}

// DecodeSFlow decodes one sFlow v5 datagram.
func DecodeSFlow(pkt []byte, exporter string, now time.Time) (*SFlowStats, error) {
	x := &xdr{b: pkt}
	ver, ok := x.u32()
	if !ok || ver != 5 {
		return nil, fmt.Errorf("sflow: version is %d, want 5", ver)
	}
	addrType, ok := x.u32()
	if !ok {
		return nil, errors.New("sflow: truncated header")
	}
	if addrType == 1 { // IPv4 agent address
		if _, ok := x.bytes(4); !ok {
			return nil, errors.New("sflow: truncated agent address")
		}
	} else if addrType == 2 { // IPv6
		if _, ok := x.bytes(16); !ok {
			return nil, errors.New("sflow: truncated agent address")
		}
	} else {
		return nil, fmt.Errorf("sflow: unknown agent address type %d", addrType)
	}
	if _, ok = x.u32(); !ok { // sub-agent id
		return nil, errors.New("sflow: truncated")
	}
	if _, ok = x.u32(); !ok { // sequence
		return nil, errors.New("sflow: truncated")
	}
	if _, ok = x.u32(); !ok { // sysUptime
		return nil, errors.New("sflow: truncated")
	}
	numSamples, ok := x.u32()
	if !ok {
		return nil, errors.New("sflow: truncated")
	}
	st := &SFlowStats{}
	for s := uint32(0); s < numSamples; s++ {
		tag, ok := x.u32()
		if !ok {
			return st, errors.New("sflow: truncated sample tag")
		}
		_, format := splitTag(tag)
		length, ok := x.u32()
		if !ok {
			return st, errors.New("sflow: truncated sample length")
		}
		body, ok := x.bytes(int(length))
		if !ok {
			return st, errors.New("sflow: truncated sample body")
		}
		if pad := (4 - (length % 4)) % 4; pad > 0 { // opaque values are padded
			if _, ok := x.bytes(int(pad)); !ok {
				return st, errors.New("sflow: truncated sample padding")
			}
		}
		sx := &xdr{b: body}
		switch format {
		case 1: // flow sample
			st.FlowSamples++
			st.Records = append(st.Records, decodeFlowSample(sx, exporter, now, false)...)
		case 3: // expanded flow sample
			st.FlowSamples++
			st.Records = append(st.Records, decodeFlowSample(sx, exporter, now, true)...)
		case 2, 4: // counter samples: counted, contents ignored by design
			st.CounterSamples++
		}
	}
	return st, nil
}

func decodeFlowSample(x *xdr, exporter string, now time.Time, expanded bool) []Record {
	if _, ok := x.u32(); !ok { // sequence number
		return nil
	}
	if expanded { // source id: type + index as separate words
		if _, ok := x.u32(); !ok {
			return nil
		}
		if _, ok := x.u32(); !ok {
			return nil
		}
	} else { // source id combined word
		if _, ok := x.u32(); !ok {
			return nil
		}
	}
	rate, ok := x.u32() // sampling rate
	if !ok || rate == 0 {
		rate = 1
	}
	if _, ok = x.u32(); !ok { // sample pool
		return nil
	}
	if _, ok = x.u32(); !ok { // drops
		return nil
	}
	if expanded { // input if: format + value
		if _, ok := x.u32(); !ok {
			return nil
		}
		if _, ok := x.u32(); !ok {
			return nil
		}
		if _, ok := x.u32(); !ok { // output if: format
			return nil
		}
		if _, ok := x.u32(); !ok { // output if: value
			return nil
		}
	} else {
		if _, ok := x.u32(); !ok { // input ifIndex
			return nil
		}
		if _, ok := x.u32(); !ok { // output ifIndex
			return nil
		}
	}
	numRecords, ok := x.u32()
	if !ok {
		return nil
	}
	var out []Record
	for i := uint32(0); i < numRecords; i++ {
		tag, ok := x.u32()
		if !ok {
			return out
		}
		_, format := splitTag(tag)
		data, ok := x.opaque()
		if !ok {
			return out
		}
		if format != 1 { // only sampled_header records are decoded
			continue
		}
		if r, ok := parseSampledHeader(data, exporter, now, rate); ok {
			out = append(out, r)
		}
	}
	return out
}

// parseSampledHeader extracts IP/transport fields from one sampled
// packet header (ethernet II framing). Byte/packet counts are scaled by
// the sampling rate, the standard sFlow volume estimate; with a sampling
// rate of 1 they are exact.
func parseSampledHeader(data []byte, exporter string, now time.Time, rate uint32) (Record, bool) {
	x := &xdr{b: data}
	protocol, ok := x.u32() // 1 = ethernet
	if !ok || protocol != 1 {
		return Record{}, false
	}
	frameLen, ok := x.u32()
	if !ok {
		return Record{}, false
	}
	if _, ok = x.u32(); !ok { // stripped octets
		return Record{}, false
	}
	hdr, ok := x.opaque()
	if !ok || len(hdr) < 14 {
		return Record{}, false
	}
	r := Record{Exporter: exporter, TS: now,
		Bytes: uint64(frameLen) * uint64(rate), Packets: uint64(rate)}
	etherType := be16(hdr[12:14])
	off := 14
	if etherType == 0x8100 && len(hdr) >= 18 { // 802.1Q VLAN tag
		etherType = be16(hdr[16:18])
		off = 18
	}
	if etherType != 0x0800 { // only IPv4 headers are decoded
		return Record{}, false
	}
	ip := hdr[off:]
	if len(ip) < 20 {
		return Record{}, false
	}
	ihl := int(ip[0]&0x0F) * 4
	if ihl < 20 || len(ip) < ihl {
		return Record{}, false
	}
	r.Proto = int(ip[9])
	r.Src = ipStr(ip[12:16])
	r.Dst = ipStr(ip[16:20])
	switch r.Proto {
	case 6, 17: // TCP, UDP
		if len(ip) >= ihl+4 {
			r.SrcPort = int(be16(ip[ihl : ihl+2]))
			r.DstPort = int(be16(ip[ihl+2 : ihl+4]))
		}
	}
	return r, true
}
