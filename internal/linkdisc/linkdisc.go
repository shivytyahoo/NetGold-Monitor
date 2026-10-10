package linkdisc

import (
	"fmt"
	"strings"
	"sync"

	"netgold/internal/poller"
	"netgold/internal/store"
)

// Index resolves neighbour identities (IP, MAC, name) to monitored
// device IDs and interface names.
type Index struct {
	ByIP   map[string]int64
	ByMAC  map[string]int64
	ByName map[string]int64
	// IfaceName[deviceID][ifIndex] = interface name.
	IfaceName map[int64]map[int]string
	// IfaceByMAC[mac] = "deviceID:ifIndex" encoded lookup for names.
	ifaceByMAC map[string][2]int64 // mac -> {deviceID, ifIndex}
	// ARP-derived identity hints gathered from every device.
	ArpIPToMAC map[string]string
	ArpMACToIP map[string]string
}

func shortName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// BuildIndex builds the identity index from devices, their interfaces
// and all collected ARP tables.
func BuildIndex(devices []store.Device, ifaces []store.Interface, arp map[int64][]ArpEntry) *Index {
	idx := &Index{
		ByIP: map[string]int64{}, ByMAC: map[string]int64{}, ByName: map[string]int64{},
		IfaceName: map[int64]map[int]string{}, ifaceByMAC: map[string][2]int64{},
		ArpIPToMAC: map[string]string{}, ArpMACToIP: map[string]string{},
	}
	for _, d := range devices {
		if d.IP != "" {
			idx.ByIP[d.IP] = d.ID
		}
		for _, n := range []string{d.SysName, d.Name} {
			if n != "" {
				idx.ByName[strings.ToLower(n)] = d.ID
				idx.ByName[shortName(n)] = d.ID
			}
		}
	}
	for _, ifc := range ifaces {
		if idx.IfaceName[ifc.DeviceID] == nil {
			idx.IfaceName[ifc.DeviceID] = map[int]string{}
		}
		idx.IfaceName[ifc.DeviceID][ifc.IfIndex] = ifc.Name
		if ifc.MAC != "" {
			idx.ByMAC[ifc.MAC] = ifc.DeviceID
			idx.ifaceByMAC[ifc.MAC] = [2]int64{ifc.DeviceID, int64(ifc.IfIndex)}
		}
	}
	for _, entries := range arp {
		for _, e := range entries {
			idx.ArpIPToMAC[e.IP] = e.MAC
			idx.ArpMACToIP[e.MAC] = e.IP
		}
	}
	return idx
}

// resolve tries, in priority order: direct IP, direct MAC, IP via ARP
// to a known interface MAC, then name/sysName. Returns 0 when the
// identity is not a monitored device.
func (idx *Index) resolve(ip, mac, name string) int64 {
	if ip != "" {
		if id, ok := idx.ByIP[ip]; ok {
			return id
		}
	}
	if mac != "" {
		if id, ok := idx.ByMAC[mac]; ok {
			return id
		}
	}
	if ip != "" {
		if m, ok := idx.ArpIPToMAC[ip]; ok {
			if id, ok2 := idx.ByMAC[m]; ok2 {
				return id
			}
		}
	}
	if mac != "" {
		if ip2, ok := idx.ArpMACToIP[mac]; ok {
			if id, ok2 := idx.ByIP[ip2]; ok2 {
				return id
			}
		}
	}
	if name != "" {
		if id, ok := idx.ByName[strings.ToLower(name)]; ok {
			return id
		}
		if id, ok := idx.ByName[shortName(name)]; ok {
			return id
		}
	}
	return 0
}

func (idx *Index) ifName(deviceID int64, ifIndex int) string {
	if m, ok := idx.IfaceName[deviceID]; ok {
		if n, ok := m[ifIndex]; ok && n != "" {
			return n
		}
	}
	if ifIndex > 0 {
		return fmt.Sprintf("if%d", ifIndex)
	}
	return ""
}

// lldpIdentity decodes an LLDP chassis id by subtype into ip/mac/name.
func lldpIdentity(r LldpRemote) (ip, mac, name string) {
	switch r.ChassisSubtype {
	case 4: // MAC address
		mac = poller.FormatMAC(r.ChassisID)
	case 5: // network address: family byte + address
		if len(r.ChassisID) >= 5 && r.ChassisID[0] == 1 {
			ip = fmt.Sprintf("%d.%d.%d.%d", r.ChassisID[1], r.ChassisID[2], r.ChassisID[3], r.ChassisID[4])
		} else if len(r.ChassisID) == 17 && r.ChassisID[0] == 2 {
			ip = fmt.Sprintf("%x", r.ChassisID[1:]) // rare; not matched against IPv4 index
		}
	case 7, 1, 2, 6: // local / component / alias / interface name: textual
		name = strings.TrimSpace(string(r.ChassisID))
	default:
		name = strings.TrimSpace(string(r.ChassisID))
	}
	if name == "" {
		name = r.SysName
	}
	return ip, mac, name
}

// lldpRemotePort renders the remote port for a link endpoint: the
// port id itself when it is a name, or the interface name resolved
// through the remote device when the port id is a MAC.
func (idx *Index) lldpRemotePort(r LldpRemote, remoteID int64) string {
	switch r.PortSubtype {
	case 3: // MAC address
		if ref, ok := idx.ifaceByMAC[poller.FormatMAC(r.PortID)]; ok {
			return idx.ifName(ref[0], int(ref[1]))
		}
		return poller.FormatMAC(r.PortID)
	case 4: // network address
		if len(r.PortID) >= 5 && r.PortID[0] == 1 {
			return fmt.Sprintf("%d.%d.%d.%d", r.PortID[1], r.PortID[2], r.PortID[3], r.PortID[4])
		}
		return ""
	default: // 1 alias, 5 interface name, 6 circuit id, 7 local, 2 component
		return strings.TrimSpace(string(r.PortID))
	}
}

// Correlate turns one device's neighbour data into discovered links.
// self is the device the data was collected from. ARP data is used only
// inside Index.resolve (identity resolution), never as link evidence.
func Correlate(self int64, data *DeviceData, idx *Index) []store.DiscoveredLink {
	var out []store.DiscoveredLink
	add := func(remote int64, ifA, ifB, via, conf string) {
		if remote == 0 || remote == self {
			return
		}
		out = append(out, store.NormaliseLink(store.DiscoveredLink{
			DeviceA: self, IfA: ifA, DeviceB: remote, IfB: ifB, Via: via, Confidence: conf,
		}))
	}
	for _, r := range data.LLDP {
		ip, mac, name := lldpIdentity(r)
		remote := idx.resolve(ip, mac, name)
		if remote == 0 && r.SysName != "" {
			remote = idx.resolve("", "", r.SysName)
		}
		if remote == 0 {
			continue
		}
		add(remote, idx.ifName(self, r.LocalPortNum), idx.lldpRemotePort(r, remote), "LLDP", "high")
	}
	for _, r := range data.CDP {
		remote := idx.resolve(r.Address, "", r.DeviceID)
		if remote == 0 {
			continue
		}
		add(remote, idx.ifName(self, r.IfIndex), r.DevicePort, "CDP", "high")
	}
	// FDB: a local port that learned the interface MACs of exactly one
	// other monitored device (and no other monitored device) is a link.
	byPort := map[int]map[int64]string{} // ifIndex -> remote device -> an ifName for it
	for _, e := range data.FDB {
		remote, ok := idx.ByMAC[e.MAC]
		if !ok || remote == self {
			continue
		}
		if byPort[e.IfIndex] == nil {
			byPort[e.IfIndex] = map[int64]string{}
		}
		ref := idx.ifaceByMAC[e.MAC]
		byPort[e.IfIndex][remote] = idx.ifName(ref[0], int(ref[1]))
	}
	for ifx, remotes := range byPort {
		if len(remotes) != 1 {
			continue
		}
		for remote, ifB := range remotes {
			add(remote, idx.ifName(self, ifx), ifB, "FDB", "low")
		}
	}
	return out
}

// Report summarises one discovery run.
type Report struct {
	DevicesPolled int            `json:"devices_polled"`
	DevicesOK     int            `json:"devices_ok"`
	ARPEntries    int            `json:"arp_entries"`
	LLDPNeighbors int            `json:"lldp_neighbors"`
	CDPNeighbors  int            `json:"cdp_neighbors"`
	FDBEntries    int            `json:"fdb_entries"`
	LinksByVia    map[string]int `json:"links_by_via"`
	LinksTotal    int            `json:"links_total"`
	Errors        []string       `json:"errors,omitempty"`
}

// Fetch walks one device's neighbour tables over a single SNMP session.
// Individual table failures are tolerated (a device without LLDP simply
// has no LLDP neighbours); only a total session failure is an error.
func Fetch(d *store.Device) (*DeviceData, error) {
	g, err := poller.NewClient(d.IP, poller.CredsOf(d))
	if err != nil {
		return nil, err
	}
	defer g.Conn.Close()
	// A reachability probe: if even sysUpTime does not answer, the whole
	// session is useless.
	if _, err := g.Get([]string{"1.3.6.1.2.1.1.3.0"}); err != nil {
		return nil, err
	}
	data := &DeviceData{DeviceID: d.ID}
	data.ARP = ParseARP(poller.WalkMap(g, oidArpTable))
	data.LLDP = ParseLLDP(poller.WalkMap(g, oidLldpRem))
	data.CDP = ParseCDP(poller.WalkMap(g, oidCdpCache))
	base := ParseBasePorts(poller.WalkMap(g, oidDot1dBase))
	data.FDB = ParseDot1dFDB(poller.WalkMap(g, oidDot1dFdb), base)
	return data, nil
}

// Run executes a full discovery round: fetch every SNMP device, build
// the identity index (including the dot1q FDB of each device, resolved
// with its own port map), correlate, and store the links.
func Run(st *store.Store) (*Report, error) {
	devices, err := st.ListDevices()
	if err != nil {
		return nil, err
	}
	ifaces, err := st.AllInterfaces()
	if err != nil {
		return nil, err
	}
	rep := &Report{LinksByVia: map[string]int{}}
	collected := map[int64]*DeviceData{}
	arpAll := map[int64][]ArpEntry{}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range devices {
		d := devices[i]
		if d.SnmpDisabled || d.Type == "demo" {
			continue
		}
		rep.DevicesPolled++
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := fetchFull(&d)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s (%s): %v", d.Name, d.IP, err))
				return
			}
			rep.DevicesOK++
			collected[d.ID] = data
			arpAll[d.ID] = data.ARP
			rep.ARPEntries += len(data.ARP)
			rep.LLDPNeighbors += len(data.LLDP)
			rep.CDPNeighbors += len(data.CDP)
			rep.FDBEntries += len(data.FDB)
		}()
	}
	wg.Wait()

	idx := BuildIndex(devices, ifaces, arpAll)
	seen := map[string]bool{}
	for id, data := range collected {
		for _, l := range Correlate(id, data, idx) {
			key := fmt.Sprintf("%d|%s|%d|%s|%s", l.DeviceA, l.IfA, l.DeviceB, l.IfB, l.Via)
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := st.UpsertDiscoveredLink(l); err != nil {
				rep.Errors = append(rep.Errors, "store link: "+err.Error())
				continue
			}
			rep.LinksByVia[l.Via]++
			rep.LinksTotal++
		}
	}
	return rep, nil
}

// fetchFull is Fetch plus the Q-BRIDGE forwarding table, which needs
// the device's own interface index set for port resolution.
func fetchFull(d *store.Device) (*DeviceData, error) {
	data, err := Fetch(d)
	if err != nil {
		return nil, err
	}
	g, err := poller.NewClient(d.IP, poller.CredsOf(d))
	if err != nil {
		return data, nil // second session is best-effort
	}
	defer g.Conn.Close()
	base := ParseBasePorts(poller.WalkMap(g, oidDot1dBase))
	known := map[int]bool{}
	// Known ifIndexes come from the dot1d FDB resolution + ARP table.
	for _, e := range data.FDB {
		known[e.IfIndex] = true
	}
	for _, e := range data.ARP {
		known[e.IfIndex] = true
	}
	data.FDB = append(data.FDB, ParseDot1qFDB(poller.WalkMap(g, oidDot1qFdb), base, known)...)
	return data, nil
}
