package poller

import (
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"netgold/internal/store"
)

const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID = "1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = "1.3.6.1.2.1.1.3.0"
	oidSysName     = "1.3.6.1.2.1.1.5.0"

	oidIfDescr      = "1.3.6.1.2.1.2.2.1.2"
	oidIfSpeed      = "1.3.6.1.2.1.2.2.1.5"
	oidIfPhysAddr   = "1.3.6.1.2.1.2.2.1.6"
	oidIfOperStatus = "1.3.6.1.2.1.2.2.1.8"
	oidIfInOctets   = "1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets  = "1.3.6.1.2.1.2.2.1.16"
	oidIfName       = "1.3.6.1.2.1.31.1.1.1.1"
	oidIfHighSpeed  = "1.3.6.1.2.1.31.1.1.1.15"
	oidIfHCInOctets = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOct   = "1.3.6.1.2.1.31.1.1.1.10"

	oidHrProcLoad = "1.3.6.1.2.1.25.3.3.1.2"
	oidHrStorage  = "1.3.6.1.2.1.25.2.3.1" // .3 descr, .4 units, .5 size, .6 used
)

type SnmpData struct {
	SysDescr    string
	SysName     string
	SysObjectID string
	UpTime      uint32 // TimeTicks (centiseconds)
	CPU         *float64
	Mem         *float64
	Ifaces      []IfaceData
}

type IfaceData struct {
	Index     int
	Name      string
	SpeedBps  int64
	OperUp    bool
	InOctets  uint64
	OutOctets uint64
	Bits      uint // counter width used (64 when HC available, else 32)
	HasCount  bool
	MAC       string // ifPhysAddress, lower-case colon format; "" when unknown
}

// FormatMAC renders a raw hardware address (6 bytes) in the canonical
// lower-case colon form used in the store and link discovery.
func FormatMAC(v any) string {
	var b []byte
	switch t := v.(type) {
	case []byte:
		b = t
	case string:
		b = []byte(t)
	default:
		return ""
	}
	if len(b) != 6 {
		return ""
	}
	parts := make([]string, 6)
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

// SnmpCreds carries everything needed to talk SNMP to one device:
// v2c community, or the SNMP v3 (USM) security settings.
type SnmpCreds struct {
	Version   string // "v2c" (default) or "v3"
	Community string
	SecName   string
	AuthProto string // "", "MD5", "SHA"
	AuthPass  string
	PrivProto string // "", "DES", "AES"
	PrivPass  string
	SecLevel  string // "noAuthNoPriv", "authNoPriv", "authPriv"
}

// CredsOf extracts the SNMP credentials from a stored device.
func CredsOf(d *store.Device) SnmpCreds {
	v := d.SNMPVersion
	if v == "" {
		v = "v2c"
	}
	return SnmpCreds{
		Version: v, Community: d.Community,
		SecName: d.V3SecName, AuthProto: d.V3AuthProto, AuthPass: d.V3AuthPass,
		PrivProto: d.V3PrivProto, PrivPass: d.V3PrivPass, SecLevel: d.V3SecLevel,
	}
}

// applyCreds maps SnmpCreds onto a gosnmp client (no network I/O — this
// mapping is unit-tested). SNMP probe defaults follow the legacy system:
// timeout 2 s, 1 retry (installer study, ActiveMonitorTypeData).
func applyCreds(g *gosnmp.GoSNMP, c SnmpCreds) {
	g.Timeout = 2 * time.Second
	g.Retries = 1
	if c.Version != "v3" {
		g.Version = gosnmp.Version2c
		g.Community = c.Community
		return
	}
	g.Version = gosnmp.Version3
	g.SecurityModel = gosnmp.UserSecurityModel
	usm := &gosnmp.UsmSecurityParameters{UserName: c.SecName}
	switch strings.ToUpper(c.AuthProto) {
	case "MD5":
		usm.AuthenticationProtocol = gosnmp.MD5
	case "SHA":
		usm.AuthenticationProtocol = gosnmp.SHA
	default:
		usm.AuthenticationProtocol = gosnmp.NoAuth
	}
	usm.AuthenticationPassphrase = c.AuthPass
	switch strings.ToUpper(c.PrivProto) {
	case "DES":
		usm.PrivacyProtocol = gosnmp.DES
	case "AES":
		usm.PrivacyProtocol = gosnmp.AES
	default:
		usm.PrivacyProtocol = gosnmp.NoPriv
	}
	usm.PrivacyPassphrase = c.PrivPass
	switch c.SecLevel {
	case "authPriv":
		g.MsgFlags = gosnmp.AuthPriv
		if usm.AuthenticationProtocol == gosnmp.NoAuth {
			// authPriv without an auth protocol is not a valid USM
			// combination; fall back to the strongest level the given
			// parameters actually support.
			g.MsgFlags = gosnmp.NoAuthNoPriv
		}
	case "authNoPriv":
		g.MsgFlags = gosnmp.AuthNoPriv
	default:
		g.MsgFlags = gosnmp.NoAuthNoPriv
	}
	g.SecurityParameters = usm
}

func snmpClient(ip string, c SnmpCreds) (*gosnmp.GoSNMP, error) {
	g := &gosnmp.GoSNMP{Target: ip, Port: 161}
	applyCreds(g, c)
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return g, nil
}

func toUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case uint32:
		return uint64(n), true
	case uint:
		return uint64(n), true
	case int:
		return uint64(n), true
	case int64:
		return uint64(n), true
	case int32:
		return uint64(n), true
	}
	return 0, false
}

func toStr(v any) string {
	switch s := v.(type) {
	case []byte:
		return string(s)
	case string:
		return s
	}
	return fmt.Sprint(v)
}

// NewClient returns a connected SNMP client for one device (the caller
// closes g.Conn). Exported for link discovery, which walks several
// neighbour tables over one session.
func NewClient(ip string, c SnmpCreds) (*gosnmp.GoSNMP, error) {
	return snmpClient(ip, c)
}

// WalkMap is walkMap for other packages: suffix-index -> value.
func WalkMap(g *gosnmp.GoSNMP, root string) map[string]any {
	return walkMap(g, root)
}

// walkMap walks root and returns suffix-index -> value.
func walkMap(g *gosnmp.GoSNMP, root string) map[string]any {
	out := map[string]any{}
	_ = g.Walk(root, func(pdu gosnmp.SnmpPDU) error {
		name := strings.TrimPrefix(pdu.Name, ".")
		rootT := strings.TrimPrefix(root, ".")
		idx := strings.TrimPrefix(strings.TrimPrefix(name, rootT), ".")
		out[idx] = pdu.Value
		return nil
	})
	return out
}

func idxOf(suffix string) int {
	var i int
	fmt.Sscanf(suffix, "%d", &i)
	return i
}

// FetchAll does one full SNMP poll with a v2c community. Error means
// SNMP unreachable (device may still be ping-up; callers decide status).
func FetchAll(ip, community string) (*SnmpData, error) {
	return FetchAllCreds(ip, SnmpCreds{Version: "v2c", Community: community})
}

// FetchAllCreds is FetchAll with explicit credentials (v2c or v3/USM).
func FetchAllCreds(ip string, creds SnmpCreds) (*SnmpData, error) {
	g, err := snmpClient(ip, creds)
	if err != nil {
		return nil, err
	}
	defer g.Conn.Close()

	res, err := g.Get([]string{oidSysDescr, oidSysObjectID, oidSysUpTime, oidSysName})
	if err != nil {
		return nil, err
	}
	d := &SnmpData{}
	for _, v := range res.Variables {
		switch strings.TrimPrefix(v.Name, ".") {
		case oidSysDescr:
			d.SysDescr = toStr(v.Value)
		case oidSysObjectID:
			d.SysObjectID = strings.TrimPrefix(toStr(v.Value), ".")
		case oidSysName:
			d.SysName = toStr(v.Value)
		case oidSysUpTime:
			if u, ok := toUint(v.Value); ok {
				d.UpTime = uint32(u)
			}
		}
	}

	// CPU: average hrProcessorLoad across cores.
	if m := walkMap(g, oidHrProcLoad); len(m) > 0 {
		sum, n := 0.0, 0
		for _, v := range m {
			if u, ok := toUint(v); ok {
				sum += float64(u)
				n++
			}
		}
		if n > 0 {
			avg := sum / float64(n)
			d.CPU = &avg
		}
	}

	// Memory: pick the HOST-RESOURCES storage row that looks like RAM.
	d.Mem = memFromStorage(g)

	// Interfaces.
	descr := walkMap(g, oidIfDescr)
	names := walkMap(g, oidIfName)
	phys := walkMap(g, oidIfPhysAddr)
	speed := walkMap(g, oidIfSpeed)
	hspeed := walkMap(g, oidIfHighSpeed)
	oper := walkMap(g, oidIfOperStatus)
	hcIn := walkMap(g, oidIfHCInOctets)
	hcOut := walkMap(g, oidIfHCOutOct)
	in32 := walkMap(g, oidIfInOctets)
	out32 := walkMap(g, oidIfOutOctets)
	seen := map[int]bool{}
	for suf := range descr {
		seen[idxOf(suf)] = true
	}
	for suf := range names {
		seen[idxOf(suf)] = true
	}
	for idx := range seen {
		s := fmt.Sprint(idx)
		ifd := IfaceData{Index: idx, Bits: 64}
		ifd.Name = toStr(descr[s])
		if n := toStr(names[s]); n != "" {
			ifd.Name = n
		}
		ifd.MAC = FormatMAC(phys[s])
		if u, ok := toUint(hspeed[s]); ok && u > 0 {
			ifd.SpeedBps = int64(u) * 1_000_000
		} else if u, ok := toUint(speed[s]); ok {
			ifd.SpeedBps = int64(u)
		}
		if u, ok := toUint(oper[s]); ok {
			ifd.OperUp = u == 1
		}
		if u, ok := toUint(hcIn[s]); ok {
			ifd.InOctets, ifd.HasCount = u, true
			if v, ok2 := toUint(hcOut[s]); ok2 {
				ifd.OutOctets = v
			}
		} else if u, ok := toUint(in32[s]); ok { // 32-bit fallback
			ifd.InOctets, ifd.HasCount, ifd.Bits = u, true, 32
			if v, ok2 := toUint(out32[s]); ok2 {
				ifd.OutOctets = v
			}
		}
		d.Ifaces = append(d.Ifaces, ifd)
	}
	return d, nil
}

func memFromStorage(g *gosnmp.GoSNMP) *float64 {
	descr := walkMap(g, oidHrStorage+".3")
	units := walkMap(g, oidHrStorage+".4")
	size := walkMap(g, oidHrStorage+".5")
	used := walkMap(g, oidHrStorage+".6")
	for suf, dv := range descr {
		name := strings.ToLower(toStr(dv))
		if !(strings.Contains(name, "ram") || strings.Contains(name, "physical memory") || strings.Contains(name, "real memory") || strings.Contains(name, "main memory")) {
			continue
		}
		sz, ok1 := toUint(size[suf])
		us, ok2 := toUint(used[suf])
		_, _ = units, ok1
		if ok1 && ok2 && sz > 0 {
			pct := float64(us) * 100 / float64(sz)
			return &pct
		}
	}
	return nil
}

// Probe is a quick sysDescr fetch used by discovery (short timeout).
func Probe(ip, community string) (sysName, sysDescr string, ok bool) {
	g := &gosnmp.GoSNMP{Target: ip, Port: 161, Community: community, Version: gosnmp.Version2c, Timeout: 1200 * time.Millisecond, Retries: 0}
	if err := g.Connect(); err != nil {
		return "", "", false
	}
	defer g.Conn.Close()
	res, err := g.Get([]string{oidSysDescr, oidSysName})
	if err != nil {
		return "", "", false
	}
	for _, v := range res.Variables {
		switch strings.TrimPrefix(v.Name, ".") {
		case oidSysDescr:
			sysDescr = toStr(v.Value)
		case oidSysName:
			sysName = toStr(v.Value)
		}
	}
	return sysName, sysDescr, true
}
