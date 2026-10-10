// Package linkdisc discovers real device-to-device links the way
// classic Layer-2 topology tools do: it walks the neighbour
// tables of every SNMP device (LLDP, CDP, bridge forwarding tables,
// ARP) and correlates the identities it finds against the monitored
// devices. ARP data alone is never treated as link evidence — it only
// helps resolve an LLDP/CDP identity (MAC <-> IP) to a device.
package linkdisc

import (
	"fmt"
	"strconv"
	"strings"

	"netgold/internal/poller"
)

// ArpEntry is one ipNetToMediaTable row.
type ArpEntry struct {
	IfIndex int
	MAC     string
	IP      string
}

// LldpRemote is one LLDP neighbour (lldpRemTable row).
type LldpRemote struct {
	LocalPortNum   int
	ChassisSubtype int
	ChassisID      []byte
	PortSubtype    int
	PortID         []byte
	SysName        string
}

// CdpRemote is one CDP neighbour (cdpCacheTable row).
type CdpRemote struct {
	IfIndex    int
	Address    string // neighbour IP, "" when not an IP address
	DeviceID   string
	DevicePort string
}

// FdbEntry is one learned forwarding-database MAC, with the bridge port
// already resolved to an ifIndex.
type FdbEntry struct {
	MAC     string
	IfIndex int
}

// DeviceData is everything link discovery collected from one device.
type DeviceData struct {
	DeviceID int64
	ARP      []ArpEntry
	LLDP     []LldpRemote
	CDP      []CdpRemote
	FDB      []FdbEntry
}

const (
	oidArpTable  = "1.3.6.1.2.1.4.22.1"         // ipNetToMediaTable
	oidLldpRem   = "1.0.8802.1.1.2.1.4.1.1"     // lldpRemEntry
	oidCdpCache  = "1.3.6.1.4.1.9.9.23.1.2.1.1" // cdpCacheEntry
	oidDot1dFdb  = "1.3.6.1.2.1.17.4.3.1"       // dot1dTpFdbEntry
	oidDot1dBase = "1.3.6.1.2.1.17.1.4.1"       // dot1dBasePortEntry
	oidDot1qFdb  = "1.3.6.1.2.1.17.7.1.2.2.1"   // dot1qTpFdbEntry
)

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case uint:
		return int(n), true
	}
	return 0, false
}

func toBytes(v any) []byte {
	switch t := v.(type) {
	case []byte:
		return t
	case string:
		return []byte(t)
	}
	return nil
}

// rowsOf splits a walk result (suffix -> value) into
// column -> index-key -> value, where the suffix is "<col>.<index...>".
func rowsOf(m map[string]any) map[int]map[string]any {
	out := map[int]map[string]any{}
	for suf, v := range m {
		colStr, rest, found := strings.Cut(suf, ".")
		if !found {
			continue
		}
		col, err := strconv.Atoi(colStr)
		if err != nil {
			continue
		}
		if out[col] == nil {
			out[col] = map[string]any{}
		}
		out[col][rest] = v
	}
	return out
}

func splitInts(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func macFromOctets(oct []int) string {
	if len(oct) != 6 {
		return ""
	}
	b := make([]byte, 6)
	for i, o := range oct {
		b[i] = byte(o)
	}
	return poller.FormatMAC(b)
}

// ParseARP turns an ipNetToMediaTable walk into entries. Index format:
// <ifIndex>.<ip1>.<ip2>.<ip3>.<ip4>; column 2 is the physical address.
func ParseARP(m map[string]any) []ArpEntry {
	rows := rowsOf(m)
	var out []ArpEntry
	for key, v := range rows[2] {
		idx := splitInts(key)
		if len(idx) != 5 {
			continue
		}
		mac := poller.FormatMAC(toBytes(v))
		if mac == "" {
			continue
		}
		out = append(out, ArpEntry{
			IfIndex: idx[0],
			MAC:     mac,
			IP:      fmt.Sprintf("%d.%d.%d.%d", idx[1], idx[2], idx[3], idx[4]),
		})
	}
	return out
}

// ParseLLDP turns an lldpRemTable walk into neighbours. Index format:
// <timeMark>.<localPortNum>.<remIndex>; columns: 4 chassis subtype,
// 5 chassis id, 6 port subtype, 7 port id, 9 system name.
func ParseLLDP(m map[string]any) []LldpRemote {
	rows := rowsOf(m)
	keys := map[string]*LldpRemote{}
	order := []string{}
	get := func(key string) *LldpRemote {
		r, ok := keys[key]
		if !ok {
			r = &LldpRemote{}
			keys[key] = r
			order = append(order, key)
		}
		return r
	}
	for key := range rows[5] {
		get(key)
	}
	for key := range rows[9] {
		get(key)
	}
	for _, key := range order {
		idx := splitInts(key)
		if len(idx) != 3 {
			continue
		}
		r := keys[key]
		r.LocalPortNum = idx[1]
		if v, ok := toInt(rows[4][key]); ok {
			r.ChassisSubtype = v
		}
		r.ChassisID = toBytes(rows[5][key])
		if v, ok := toInt(rows[6][key]); ok {
			r.PortSubtype = v
		}
		r.PortID = toBytes(rows[7][key])
		if b := toBytes(rows[9][key]); b != nil {
			r.SysName = string(b)
		}
	}
	out := make([]LldpRemote, 0, len(order))
	for _, key := range order {
		if r := keys[key]; r.ChassisID != nil || r.SysName != "" {
			out = append(out, *r)
		}
	}
	return out
}

// ParseCDP turns a cdpCacheTable walk into neighbours. Index format:
// <ifIndex>.<deviceIndex>; columns: 4 address type, 5 address,
// 7 device id, 8 device port.
func ParseCDP(m map[string]any) []CdpRemote {
	rows := rowsOf(m)
	keys := map[string]*CdpRemote{}
	for key := range rows[7] {
		idx := splitInts(key)
		if len(idx) != 2 {
			continue
		}
		r := &CdpRemote{IfIndex: idx[0]}
		if at, ok := toInt(rows[4][key]); ok && at == 1 { // 1 = IP
			if b := toBytes(rows[5][key]); len(b) == 4 {
				r.Address = fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
			}
		}
		if b := toBytes(rows[7][key]); b != nil {
			r.DeviceID = string(b)
		}
		if b := toBytes(rows[8][key]); b != nil {
			r.DevicePort = string(b)
		}
		keys[key] = r
	}
	out := make([]CdpRemote, 0, len(keys))
	for _, r := range keys {
		if r.DeviceID != "" || r.Address != "" {
			out = append(out, *r)
		}
	}
	return out
}

// ParseBasePorts maps dot1dBasePort -> ifIndex (column 2).
func ParseBasePorts(m map[string]any) map[int]int {
	rows := rowsOf(m)
	out := map[int]int{}
	for key, v := range rows[2] {
		bp, err := strconv.Atoi(key)
		if err != nil {
			continue
		}
		if ifx, ok := toInt(v); ok {
			out[bp] = ifx
		}
	}
	return out
}

// ParseDot1dFDB turns a dot1dTpFdbTable walk into entries with the
// bridge port resolved to an ifIndex via basePorts. Index format: the
// six MAC octets; column 2 is the bridge port.
func ParseDot1dFDB(m map[string]any, basePorts map[int]int) []FdbEntry {
	rows := rowsOf(m)
	var out []FdbEntry
	for key, v := range rows[2] {
		mac := macFromOctets(splitInts(key))
		bp, ok := toInt(v)
		if mac == "" || !ok {
			continue
		}
		ifx, ok := basePorts[bp]
		if !ok {
			continue
		}
		out = append(out, FdbEntry{MAC: mac, IfIndex: ifx})
	}
	return out
}

// ParseDot1qFDB turns a dot1qTpFdbTable walk into entries. Index format:
// <vlan>.<six MAC octets>; column 2 is the bridge port. The port is
// resolved through basePorts when present, else accepted when it is
// itself a known ifIndex (common Q-BRIDGE behaviour).
func ParseDot1qFDB(m map[string]any, basePorts map[int]int, knownIfIndexes map[int]bool) []FdbEntry {
	rows := rowsOf(m)
	var out []FdbEntry
	for key, v := range rows[2] {
		parts := splitInts(key)
		if len(parts) != 7 {
			continue
		}
		mac := macFromOctets(parts[1:])
		bp, ok := toInt(v)
		if mac == "" || !ok {
			continue
		}
		ifx, ok := basePorts[bp]
		if !ok && knownIfIndexes[bp] {
			ifx, ok = bp, true
		}
		if !ok {
			continue
		}
		out = append(out, FdbEntry{MAC: mac, IfIndex: ifx})
	}
	return out
}
