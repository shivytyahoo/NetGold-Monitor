// Package devicon is the single source of truth for how a device type
// becomes a map icon. It maps the legacy system device type (its display
// name and the sMapViewIcon file basename stored by legacy) to one of
// NetGold's built-in SVG icon ids plus an accent colour. The web UI
// renders the icon artwork for the id; the mapping itself lives here so
// it is unit-tested and shared by every API that describes a device
// (devices, maps, topologies).
package devicon

import "strings"

// Icon is a resolved icon choice: ID selects the artwork in the web UI,
// Accent is the colour used for small highlights (legend chips, list
// markers).
type Icon struct {
	ID     string `json:"id"`
	Accent string `json:"accent"`
}

// Generic is the fallback icon for unrecognised types.
var Generic = Icon{ID: "generic", Accent: "#8fa3bf"}

// byBasename maps a legacy sMapViewIcon file basename (lower-cased, no
// extension) to an icon. The basename is legacy's own choice of picture for
// the type, so it wins over name patterns.
var byBasename = map[string]Icon{
	"router":                 {ID: "router", Accent: "#4da3ff"},
	"switch":                 {ID: "switch", Accent: "#38c6d9"},
	"hub":                    {ID: "hub", Accent: "#7fd4a8"},
	"bridge":                 {ID: "switch", Accent: "#38c6d9"},
	"juniper mx80":           {ID: "juniper", Accent: "#3fd68f"},
	"download":               {ID: "juniper", Accent: "#3fd68f"}, // legacy ships this odd name for "Juniper"
	"ascendpipeline":         {ID: "ascend", Accent: "#ffb84d"},
	"linuxserver":            {ID: "linux-server", Accent: "#ffa726"},
	"linuxworkstation":       {ID: "workstation", Accent: "#b0bec5"},
	"unixserver":             {ID: "server", Accent: "#90a4ae"},
	"unixworkstation":        {ID: "workstation", Accent: "#b0bec5"},
	"server":                 {ID: "server", Accent: "#90a4ae"},
	"ntserver":               {ID: "windows-server", Accent: "#4da3ff"},
	"ntdomaincontroller":     {ID: "windows-server", Accent: "#4da3ff"},
	"windows200xserver":      {ID: "windows-server", Accent: "#4da3ff"},
	"windows200xworkstation": {ID: "workstation", Accent: "#b0bec5"},
	"windows9xworkstation":   {ID: "workstation", Accent: "#b0bec5"},
	"windowsxpworkstation":   {ID: "workstation", Accent: "#b0bec5"},
	"ntworkstation":          {ID: "workstation", Accent: "#b0bec5"},
	"oldntworkstation":       {ID: "workstation", Accent: "#b0bec5"},
	"workstation":            {ID: "workstation", Accent: "#b0bec5"},
	"workstation2":           {ID: "generic", Accent: "#8fa3bf"},
	"laptop":                 {ID: "workstation", Accent: "#b0bec5"},
	"mailserver":             {ID: "server", Accent: "#90a4ae"},
	"ftpserver":              {ID: "server", Accent: "#90a4ae"},
	"webserver":              {ID: "server", Accent: "#90a4ae"},
	"databaseserver":         {ID: "server", Accent: "#90a4ae"},
	"applicationserver":      {ID: "server", Accent: "#90a4ae"},
	"redhat":                 {ID: "linux-server", Accent: "#ffa726"},
	"printer":                {ID: "printer", Accent: "#ce93d8"},
	"hpprinter":              {ID: "printer", Accent: "#ce93d8"},
	"apcdevice":              {ID: "ups", Accent: "#ffd54f"},
	"wirelessaccesspoint":    {ID: "wireless-ap", Accent: "#64d8ff"},
	"modemdslcable":          {ID: "ascend", Accent: "#ffb84d"},
	"gateway":                {ID: "router", Accent: "#4da3ff"},
	"ibmdevice":              {ID: "generic", Accent: "#8fa3bf"},
	"ciscodevice":            {ID: "router", Accent: "#4da3ff"},
	"cisco-3800-me":          {ID: "router", Accent: "#4da3ff"},
	"hpdevice":               {ID: "generic", Accent: "#8fa3bf"},
	"novelldevice":           {ID: "server", Accent: "#90a4ae"},
	"sundevice":              {ID: "server", Accent: "#90a4ae"},
	"3comdevice":             {ID: "switch", Accent: "#38c6d9"},
	"appledevice":            {ID: "workstation", Accent: "#b0bec5"},
	"motoroladevice":         {ID: "generic", Accent: "#8fa3bf"},
	"compaqdevice":           {ID: "server", Accent: "#90a4ae"},
	"microsoftdevice":        {ID: "windows-server", Accent: "#4da3ff"},
	"inteldevice":            {ID: "generic", Accent: "#8fa3bf"},
	"dlinkdevice":            {ID: "switch", Accent: "#38c6d9"},
	"linksysdevice":          {ID: "router", Accent: "#4da3ff"},
}

// namePatterns are tried in order when the icon basename is unknown or
// absent (e.g. types classified by SNMP, or hand-entered types). The
// first pattern whose needle appears in the lower-cased type name wins,
// so more specific names ("juniper", "firewall") precede generic ones
// ("server", "router").
var namePatterns = []struct {
	needle string
	icon   Icon
}{
	{"juniper", Icon{ID: "juniper", Accent: "#3fd68f"}},
	{"ascend", Icon{ID: "ascend", Accent: "#ffb84d"}},
	{"firewall", Icon{ID: "firewall", Accent: "#ff7043"}},
	{"load balancer", Icon{ID: "loadbalancer", Accent: "#ba68c8"}},
	{"balancer", Icon{ID: "loadbalancer", Accent: "#ba68c8"}},
	{"wireless", Icon{ID: "wireless-ap", Accent: "#64d8ff"}},
	{"access point", Icon{ID: "wireless-ap", Accent: "#64d8ff"}},
	{"ups", Icon{ID: "ups", Accent: "#ffd54f"}},
	{"printer", Icon{ID: "printer", Accent: "#ce93d8"}},
	{"pbx", Icon{ID: "phone", Accent: "#a5d6a7"}},
	{"phone", Icon{ID: "phone", Accent: "#a5d6a7"}},
	{"polycom", Icon{ID: "phone", Accent: "#a5d6a7"}},
	{"cloud", Icon{ID: "cloud", Accent: "#90caf9"}},
	{"linux", Icon{ID: "linux-server", Accent: "#ffa726"}},
	{"windows", Icon{ID: "windows-server", Accent: "#4da3ff"}},
	{"router", Icon{ID: "router", Accent: "#4da3ff"}},
	{"gateway", Icon{ID: "router", Accent: "#4da3ff"}},
	{"switch", Icon{ID: "switch", Accent: "#38c6d9"}},
	{"hub", Icon{ID: "hub", Accent: "#7fd4a8"}},
	{"server", Icon{ID: "server", Accent: "#90a4ae"}},
	{"workstation", Icon{ID: "workstation", Accent: "#b0bec5"}},
	{"laptop", Icon{ID: "workstation", Accent: "#b0bec5"}},
	{"host", Icon{ID: "server", Accent: "#90a4ae"}},
}

// For resolves the icon for a device type. typeName is the legacy
// DeviceType display name (e.g. "Juniper MX104"); iconBasename is the
// sMapViewIcon file name (e.g. "Juniper MX80.png"). Either may be empty.
// A third fallback input, pollerType (the devices.type classification),
// is accepted via ForDevice by callers that have it.
func For(typeName, iconBasename string) Icon {
	if ic, ok := byBasename[basenameKey(iconBasename)]; ok {
		return ic
	}
	return ForName(typeName)
}

// ForName resolves an icon from a free-form type name alone (used for
// topology nodes and SNMP-classified devices that carry no legacy icon).
func ForName(typeName string) Icon {
	n := strings.ToLower(strings.TrimSpace(typeName))
	if n == "" || n == "unknown" {
		return Generic
	}
	for _, p := range namePatterns {
		if strings.Contains(n, p.needle) {
			return p.icon
		}
	}
	return Generic
}

// ForDevice resolves the icon for a stored device: the legacy type/icon
// pair wins; when the device has no imported legacy type, the poller's
// classification in devices.type is used so SNMP-discovered devices
// still get a sensible picture.
func ForDevice(deviceType, typeIcon, pollerType string) Icon {
	if strings.TrimSpace(deviceType) != "" || strings.TrimSpace(typeIcon) != "" {
		return For(deviceType, typeIcon)
	}
	return ForName(pollerType)
}

// basenameKey normalises an sMapViewIcon value ("Juniper MX80.png",
// "AscendPipeline.emf", "Cisco-3800-ME.jpg") to its lookup key.
func basenameKey(icon string) string {
	s := strings.TrimSpace(strings.ToLower(icon))
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "."); i > 0 {
		s = s[:i]
	}
	return s
}
