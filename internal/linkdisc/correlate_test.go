package linkdisc

import (
	"testing"

	"netgold/internal/store"
)

func fixture() (devices []store.Device, ifaces []store.Interface) {
	devices = []store.Device{
		{ID: 1, Name: "SW-A", IP: "10.0.0.1", SysName: "SW-A"},
		{ID: 2, Name: "SW-B", IP: "10.0.0.2", SysName: "SW-B"},
		{ID: 3, Name: "SW-C", IP: "10.0.0.3", SysName: "SW-C"},
		{ID: 4, Name: "SW-D", IP: "10.0.0.4", SysName: "SW-D"},
	}
	ifaces = []store.Interface{
		{DeviceID: 1, IfIndex: 1, Name: "Gi1/0/1", MAC: "aa:00:00:00:00:01"},
		{DeviceID: 1, IfIndex: 2, Name: "Gi1/0/2", MAC: "aa:00:00:00:00:02"},
		{DeviceID: 2, IfIndex: 1, Name: "Gi0/1", MAC: "bb:00:00:00:00:01"},
		{DeviceID: 2, IfIndex: 2, Name: "Gi0/2", MAC: "bb:00:00:00:00:02"},
		{DeviceID: 3, IfIndex: 1, Name: "Gi0/1", MAC: "cc:00:00:00:00:01"},
		{DeviceID: 4, IfIndex: 1, Name: "Gi0/1", MAC: "dd:00:00:00:00:01"},
	}
	return devices, ifaces
}

func macBytes(s string) []byte {
	var b []byte
	cur := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ':' {
			continue
		}
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = int(c-'A') + 10
		}
		if (i % 3) == 0 {
			cur = v
		} else if (i % 3) == 1 {
			b = append(b, byte(cur*16+v))
		}
	}
	return b
}

func linkKeys(links []store.DiscoveredLink) map[string]store.DiscoveredLink {
	out := map[string]store.DiscoveredLink{}
	for _, l := range links {
		l = store.NormaliseLink(l)
		key := string(rune('0'+l.DeviceA)) + "-" + string(rune('0'+l.DeviceB)) + "-" + l.Via
		out[key] = l
	}
	return out
}

// Two switches advertising each other over LLDP must merge into ONE
// normalised link.
func TestCorrelateLLDPMutualMerges(t *testing.T) {
	devices, ifaces := fixture()
	arp := map[int64][]ArpEntry{}
	idx := BuildIndex(devices, ifaces, arp)

	aData := &DeviceData{DeviceID: 1, LLDP: []LldpRemote{{
		LocalPortNum: 1, ChassisSubtype: 4, ChassisID: macBytes("bb:00:00:00:00:01"),
		PortSubtype: 5, PortID: []byte("Gi0/1"), SysName: "SW-B",
	}}}
	bData := &DeviceData{DeviceID: 2, LLDP: []LldpRemote{{
		LocalPortNum: 1, ChassisSubtype: 4, ChassisID: macBytes("aa:00:00:00:00:01"),
		PortSubtype: 5, PortID: []byte("Gi1/0/1"), SysName: "SW-A",
	}}}
	var all []store.DiscoveredLink
	all = append(all, Correlate(1, aData, idx)...)
	all = append(all, Correlate(2, bData, idx)...)
	keys := linkKeys(all)
	if len(keys) != 1 {
		t.Fatalf("mutual LLDP produced %d unique links, want 1: %+v", len(keys), all)
	}
	l := keys["1-2-LLDP"]
	if l.IfA != "Gi1/0/1" || l.IfB != "Gi0/1" || l.Confidence != "high" {
		t.Errorf("link endpoints: %+v", l)
	}
}

// CDP-only neighbour (identified by its IP address) is a link.
func TestCorrelateCDP(t *testing.T) {
	devices, ifaces := fixture()
	idx := BuildIndex(devices, ifaces, nil)
	cData := &DeviceData{DeviceID: 3, CDP: []CdpRemote{{
		IfIndex: 1, Address: "10.0.0.4", DeviceID: "SW-D", DevicePort: "GigabitEthernet0/1",
	}}}
	links := Correlate(3, cData, idx)
	keys := linkKeys(links)
	l, ok := keys["3-4-CDP"]
	if !ok {
		t.Fatalf("no CDP link found: %+v", links)
	}
	if l.IfA != "Gi0/1" || l.IfB != "GigabitEthernet0/1" {
		t.Errorf("link endpoints: %+v", l)
	}
}

// FDB: a port that learned exactly one other device's interface MAC is
// a (low-confidence) link; a port with two known devices is not.
func TestCorrelateFDB(t *testing.T) {
	devices, ifaces := fixture()
	idx := BuildIndex(devices, ifaces, nil)
	aData := &DeviceData{DeviceID: 1, FDB: []FdbEntry{
		{MAC: "bb:00:00:00:00:02", IfIndex: 2}, // SW-B's second port only
	}}
	links := Correlate(1, aData, idx)
	keys := linkKeys(links)
	l, ok := keys["1-2-FDB"]
	if !ok {
		t.Fatalf("no FDB link found: %+v", links)
	}
	if l.Confidence != "low" || l.IfA != "Gi1/0/2" || l.IfB != "Gi0/2" {
		t.Errorf("link: %+v", l)
	}

	ambiguous := &DeviceData{DeviceID: 1, FDB: []FdbEntry{
		{MAC: "bb:00:00:00:00:02", IfIndex: 2},
		{MAC: "cc:00:00:00:00:01", IfIndex: 2}, // two devices on the port
	}}
	if got := Correlate(1, ambiguous, idx); len(got) != 0 {
		t.Errorf("ambiguous FDB port produced links: %+v", got)
	}
}

// ARP alone must NEVER create a link.
func TestCorrelateARPOnlyNoLink(t *testing.T) {
	devices, ifaces := fixture()
	arp := map[int64][]ArpEntry{
		4: {
			{IfIndex: 1, MAC: "aa:00:00:00:00:01", IP: "10.0.0.1"},
			{IfIndex: 1, MAC: "bb:00:00:00:00:01", IP: "10.0.0.2"},
		},
	}
	idx := BuildIndex(devices, ifaces, arp)
	dData := &DeviceData{DeviceID: 4, ARP: arp[4]}
	if got := Correlate(4, dData, idx); len(got) != 0 {
		t.Errorf("ARP-only data produced links: %+v", got)
	}
}

// An LLDP neighbour identified only by a chassis MAC that is not an
// interface MAC can still resolve via ARP (MAC -> device IP).
func TestCorrelateLLDPResolvesViaARP(t *testing.T) {
	devices, ifaces := fixture()
	// SW-B's chassis MAC is NOT one of its interface MACs here, but SW-A's
	// ARP table maps it to SW-B's IP.
	chassisMAC := "bb:ff:ff:ff:ff:01"
	arp := map[int64][]ArpEntry{
		1: {{IfIndex: 1, MAC: chassisMAC, IP: "10.0.0.2"}},
	}
	idx := BuildIndex(devices, ifaces, arp)
	aData := &DeviceData{DeviceID: 1, LLDP: []LldpRemote{{
		LocalPortNum: 1, ChassisSubtype: 4, ChassisID: macBytes(chassisMAC),
		PortSubtype: 7, PortID: []byte("Gi0/1"),
	}}}
	links := Correlate(1, aData, idx)
	if _, ok := linkKeys(links)["1-2-LLDP"]; !ok {
		t.Fatalf("LLDP-via-ARP produced no link: %+v", links)
	}
}

// Table parsers: a small fake dot1d walk becomes FDB entries with the
// bridge port resolved to an ifIndex.
func TestParseFDBTables(t *testing.T) {
	base := ParseBasePorts(map[string]any{"2.5": 12})
	if base[5] != 12 {
		t.Fatalf("base ports: %v", base)
	}
	fdb := ParseDot1dFDB(map[string]any{
		"2.0.187.0.0.0.2": 5, // MAC 00:bb:00:00:00:02 on bridge port 5
		"3.0.187.0.0.0.2": 3, // status column, ignored
	}, base)
	if len(fdb) != 1 || fdb[0].MAC != "00:bb:00:00:00:02" || fdb[0].IfIndex != 12 {
		t.Errorf("fdb: %+v", fdb)
	}
	arp := ParseARP(map[string]any{"2.7.192.168.10.20": []byte{0x00, 0xbb, 0x00, 0x00, 0x00, 0x02}})
	if len(arp) != 1 || arp[0].IfIndex != 7 || arp[0].IP != "192.168.10.20" || arp[0].MAC != "00:bb:00:00:00:02" {
		t.Errorf("arp: %+v", arp)
	}
}
