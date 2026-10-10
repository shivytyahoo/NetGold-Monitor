package poller

import "strings"

// Device classification by SNMP sysObjectID enterprise prefix — a modest
// subset of the legacy system's seeded 49-row DeviceType table (installer
// study: each row keys on the sysObjectID enterprise prefix and assigns a
// vendor/device type). Full role templates are a later phase; this table
// only labels devices whose type is still generic.
var sysObjectIDTypes = []struct {
	prefix string
	typ    string
}{
	{"1.3.6.1.4.1.9", "Cisco device"},
	{"1.3.6.1.4.1.11", "HP device"},
	{"1.3.6.1.4.1.311", "Microsoft device"},
	{"1.3.6.1.4.1.318", "APC UPS"},
	{"1.3.6.1.4.1.2021", "Linux server (UCD-SNMP)"},
	{"1.3.6.1.4.1.8072", "Linux server (Net-SNMP)"},
	{"1.3.6.1.4.1.2", "IBM device"},
	{"1.3.6.1.4.1.25506", "H3C / Huawei device"},
	{"1.3.6.1.4.1.2011", "Huawei device"},
	{"1.3.6.1.4.1.2636", "Juniper device"},
	{"1.3.6.1.4.1.6027", "Dell device"},
	{"1.3.6.1.4.1.674", "Dell device"},
	{"1.3.6.1.4.1.171", "D-Link device"},
	{"1.3.6.1.4.1.14988", "MikroTik router"},
	{"1.3.6.1.4.1.12356", "Fortinet firewall"},
	{"1.3.6.1.4.1.529", "Ascend device"},
	{"1.3.6.1.4.1.43", "3Com / H3C device"},
	{"1.3.6.1.4.1.4526", "Netgear device"},
	{"1.3.6.1.4.1.14179", "Cisco wireless controller"},
	{"1.3.6.1.4.1.2435", "Brother printer"},
	{"1.3.6.1.4.1.1602", "Canon printer"},
	{"1.3.6.1.4.1.2385", "Kyocera printer"},
}

// ClassifySysObjectID returns the device type for a sysObjectID value
// (longest matching enterprise prefix wins), or "" when unknown.
func ClassifySysObjectID(oid string) string {
	oid = strings.TrimPrefix(strings.TrimSpace(oid), ".")
	bestLen := -1
	best := ""
	for _, row := range sysObjectIDTypes {
		if oid == row.prefix || strings.HasPrefix(oid, row.prefix+".") {
			if len(row.prefix) > bestLen {
				bestLen = len(row.prefix)
				best = row.typ
			}
		}
	}
	return best
}
