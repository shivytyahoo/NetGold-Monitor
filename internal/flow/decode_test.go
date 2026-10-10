package flow

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

var testNow = time.Unix(1_700_000_000, 0)

func u16(b []byte, off int, v uint16) { binary.BigEndian.PutUint16(b[off:], v) }
func u32(b []byte, off int, v uint32) { binary.BigEndian.PutUint32(b[off:], v) }

func buildV5() []byte {
	pkt := make([]byte, 24+48*2)
	u16(pkt, 0, 5) // version
	u16(pkt, 2, 2) // count
	// record 1
	r := pkt[24:]
	copy(r[0:4], net.ParseIP("10.0.0.1").To4())
	copy(r[4:8], net.ParseIP("10.0.0.2").To4())
	u32(r, 16, 100)   // packets
	u32(r, 20, 64000) // octets
	u16(r, 32, 51000) // src port
	u16(r, 34, 443)   // dst port
	r[38] = 6         // TCP
	// record 2
	r = pkt[24+48:]
	copy(r[0:4], net.ParseIP("192.168.1.5").To4())
	copy(r[4:8], net.ParseIP("8.8.8.8").To4())
	u32(r, 16, 3)
	u32(r, 20, 210)
	u16(r, 32, 3333)
	u16(r, 34, 53)
	r[38] = 17 // UDP
	return pkt
}

func TestDecodeNetFlowV5(t *testing.T) {
	recs, err := DecodeNetFlowV5(buildV5(), "10.9.9.9", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	r0 := recs[0]
	if r0.Src != "10.0.0.1" || r0.Dst != "10.0.0.2" || r0.Bytes != 64000 || r0.Packets != 100 ||
		r0.SrcPort != 51000 || r0.DstPort != 443 || r0.Proto != 6 || r0.Exporter != "10.9.9.9" {
		t.Errorf("record 1 mismatch: %+v", r0)
	}
	r1 := recs[1]
	if r1.Src != "192.168.1.5" || r1.Dst != "8.8.8.8" || r1.Bytes != 210 || r1.Packets != 3 ||
		r1.SrcPort != 3333 || r1.DstPort != 53 || r1.Proto != 17 {
		t.Errorf("record 2 mismatch: %+v", r1)
	}
}

// Template shared by the v9 and IPFIX fixtures: src, dst, sport, dport,
// proto, bytes, packets (21-byte records).
func templateFields() [][2]uint16 {
	return [][2]uint16{{8, 4}, {12, 4}, {7, 2}, {11, 2}, {4, 1}, {1, 4}, {2, 4}}
}

func templateRecordBody() []byte {
	var rec []byte
	tmp := make([]byte, 4)
	put := func(b []byte) { rec = append(rec, b...) }
	put(net.ParseIP("172.16.0.10").To4())
	put(net.ParseIP("172.16.0.20").To4())
	u16(tmp, 0, 51001)
	put(tmp[:2])
	u16(tmp, 0, 443)
	put(tmp[:2])
	put([]byte{6})
	u32(tmp, 0, 123456)
	put(tmp[:4])
	u32(tmp, 0, 81)
	put(tmp[:4])
	return rec // 21 bytes
}

func buildV9(withData bool) []byte {
	var pkt []byte
	hdr := make([]byte, 20)
	u16(hdr, 0, 9)
	u32(hdr, 16, 7) // source id
	pkt = append(pkt, hdr...)
	// template flowset (id 0)
	var tf []byte
	tf = append(tf, 0, 0, 0, 0) // id + length placeholder
	tmp := make([]byte, 4)
	u16(tmp, 0, 256) // template id
	u16(tmp, 2, uint16(len(templateFields())))
	tf = append(tf, tmp...)
	for _, f := range templateFields() {
		u16(tmp, 0, f[0])
		u16(tmp, 2, f[1])
		tf = append(tf, tmp...)
	}
	u16(tf, 2, uint16(len(tf)))
	pkt = append(pkt, tf...)
	if withData {
		var df []byte
		df = append(df, 0, 0, 0, 0)
		df = append(df, templateRecordBody()...)
		df = append(df, templateRecordBody()...)
		for len(df)%4 != 0 {
			df = append(df, 0)
		}
		u16(df, 0, 256)
		u16(df, 2, uint16(len(df)))
		pkt = append(pkt, df...)
	}
	return pkt
}

func TestDecodeNetFlowV9TemplateAndData(t *testing.T) {
	c := NewTemplateCache()
	// Template first (as real exporters send), then data.
	if _, err := c.DecodeTemplatePacket(buildV9(false), "10.9.9.9", testNow); err != nil {
		t.Fatal(err)
	}
	recs, err := c.DecodeTemplatePacket(buildV9(true), "10.9.9.9", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	r := recs[0]
	if r.Src != "172.16.0.10" || r.Dst != "172.16.0.20" || r.SrcPort != 51001 || r.DstPort != 443 ||
		r.Proto != 6 || r.Bytes != 123456 || r.Packets != 81 {
		t.Errorf("v9 record mismatch: %+v", r)
	}
}

func buildIPFIX() []byte {
	var pkt []byte
	hdr := make([]byte, 16)
	u16(hdr, 0, 10)
	u32(hdr, 12, 3) // domain id
	pkt = append(pkt, hdr...)
	tmp := make([]byte, 4)
	// template set (id 2), template 300
	var ts []byte
	ts = append(ts, 0, 0, 0, 0)
	u16(tmp, 0, 300)
	u16(tmp, 2, uint16(len(templateFields())))
	ts = append(ts, tmp...)
	for _, f := range templateFields() {
		u16(tmp, 0, f[0])
		u16(tmp, 2, f[1])
		ts = append(ts, tmp...)
	}
	u16(ts, 0, 2)
	u16(ts, 2, uint16(len(ts)))
	pkt = append(pkt, ts...)
	// data set 300 with one record
	var ds []byte
	ds = append(ds, 0, 0, 0, 0)
	ds = append(ds, templateRecordBody()...)
	u16(ds, 0, 300)
	u16(ds, 2, uint16(len(ds)))
	pkt = append(pkt, ds...)
	u16(pkt, 2, uint16(len(pkt))) // total message length
	return pkt
}

func TestDecodeIPFIX(t *testing.T) {
	c := NewTemplateCache()
	recs, err := c.DecodeTemplatePacket(buildIPFIX(), "10.9.9.9", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]
	if r.Src != "172.16.0.10" || r.Dst != "172.16.0.20" || r.DstPort != 443 || r.Bytes != 123456 || r.Packets != 81 {
		t.Errorf("ipfix record mismatch: %+v", r)
	}
}

func buildSFlow() []byte {
	// Sampled packet: ethernet + IPv4 + TCP, frame length 1500.
	hdr := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, // dst mac
		0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, // src mac
		0x08, 0x00, // ethertype IPv4
		0x45, 0x00, 0x05, 0xdc, 0x00, 0x01, 0x00, 0x00, 0x40, 0x06, 0x00, 0x00, // IP hdr (ttl 64, proto TCP)
		172, 16, 0, 10, // src
		172, 16, 0, 20, // dst
		0xc7, 0x39, 0x01, 0xbb, // TCP ports 51001 -> 443
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // rest of TCP header
	}
	// flow record: sampled_header (format 1), opaque data
	var rec []byte
	tmp := make([]byte, 4)
	w32 := func(dst []byte, v uint32) []byte { u32(tmp, 0, v); return append(dst, tmp...) }
	rec = w32(rec, 1) // record type: enterprise 0, format 1
	var recData []byte
	recData = w32(recData, 1)    // header protocol: ethernet
	recData = w32(recData, 1500) // frame length
	recData = w32(recData, 0)    // stripped
	recData = w32(recData, uint32(len(hdr)))
	recData = append(recData, hdr...)
	for len(recData)%4 != 0 {
		recData = append(recData, 0)
	}
	rec = w32(rec, uint32(len(recData)))
	rec = append(rec, recData...)
	// flow sample (format 1)
	var sample []byte
	sample = w32(sample, 1) // sample type: enterprise 0, format 1
	var body []byte
	body = w32(body, 7)   // sequence
	body = w32(body, 2)   // source id (type 0, index 2)
	body = w32(body, 1)   // sampling rate 1
	body = w32(body, 100) // sample pool
	body = w32(body, 0)   // drops
	body = w32(body, 3)   // input ifIndex
	body = w32(body, 4)   // output ifIndex
	body = w32(body, 1)   // 1 flow record
	body = append(body, rec...)
	sample = w32(sample, uint32(len(body)))
	sample = append(sample, body...)
	// a counter sample (format 2) with dummy content
	var csample []byte
	csample = w32(csample, 2)
	csample = w32(csample, 8)
	csample = append(csample, make([]byte, 8)...)
	sample = append(sample, csample...)
	// datagram header
	var pkt []byte
	pkt = w32(pkt, 5) // version
	pkt = w32(pkt, 1) // agent address type IPv4
	pkt = append(pkt, net.ParseIP("10.9.9.9").To4()...)
	pkt = w32(pkt, 0) // sub agent
	pkt = w32(pkt, 1) // sequence
	pkt = w32(pkt, 999)
	pkt = w32(pkt, 2) // 2 samples
	pkt = append(pkt, sample...)
	return pkt
}

func TestDecodeSFlow(t *testing.T) {
	st, err := DecodeSFlow(buildSFlow(), "10.9.9.9", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if st.FlowSamples != 1 || st.CounterSamples != 1 {
		t.Errorf("samples: flow=%d counters=%d, want 1/1", st.FlowSamples, st.CounterSamples)
	}
	if len(st.Records) != 1 {
		t.Fatalf("got %d records, want 1", len(st.Records))
	}
	r := st.Records[0]
	if r.Src != "172.16.0.10" || r.Dst != "172.16.0.20" || r.SrcPort != 51001 || r.DstPort != 443 ||
		r.Proto != 6 || r.Bytes != 1500 || r.Packets != 1 {
		t.Errorf("sflow record mismatch: %+v", r)
	}
}
