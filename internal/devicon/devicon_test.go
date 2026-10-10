package devicon

import "testing"

func TestForHisFleet(t *testing.T) {
	// The nine types in Shiv's real fleet, with the exact icon file
	// basenames his legacy database stores for them.
	cases := []struct {
		typeName, basename, wantID string
	}{
		{"Router", "Router.png", "router"},
		{"Switch", "Switch.png", "switch"},
		{"Juniper MX104", "Juniper MX80.png", "juniper"},
		{"Ascend Pipeline", "AscendPipeline.emf", "ascend"},
		{"Juniper MX-80", "Juniper MX80.png", "juniper"},
		{"Linux Server", "LinuxServer.png", "linux-server"},
		{"Server", "Server.png", "server"},
		{"Windows 2003 Server", "Windows200xServer.png", "windows-server"},
		{"Polycom Device", "Workstation.png", "workstation"},
	}
	for _, c := range cases {
		if got := For(c.typeName, c.basename); got.ID != c.wantID {
			t.Errorf("For(%q, %q) = %q, want %q", c.typeName, c.basename, got.ID, c.wantID)
		}
	}
}

func TestForCatalogue(t *testing.T) {
	cases := []struct {
		typeName, basename, wantID string
	}{
		// Catalogue rows from legacy's 70-type DeviceType table.
		{"Wireless Access Point", "WirelessAccessPoint.png", "wireless-ap"},
		{"UPS Device", "APCDevice.png", "ups"},
		{"APC Device", "APCDevice.png", "ups"},
		{"HP Printer", "HPPrinter.png", "printer"},
		{"Printer", "Printer.png", "printer"},
		{"Hub", "Hub.png", "hub"},
		{"Windows Domain Controller", "NTDomainController.png", "windows-server"},
		{"Cisco catalyst2960G48 Switch", "Switch.png", "switch"},
		{"Cisco cisco7206VXR Router", "Router.png", "router"},
		{"Juniper", "download.png", "juniper"},
		{"Juniper Juniper MX80 Router", "Router.png", "router"}, // basename wins over name
		{"Host", "Server.png", "server"},
		{"Modem (DSL/Cable)", "ModemDSLCable.png", "ascend"},
		{"Gateway", "Gateway.png", "router"},
		// Name-pattern coverage for types with no stored icon.
		{"Fortinet firewall", "", "firewall"},
		{"Palo Alto Firewall", "", "firewall"},
		{"F5 Load Balancer", "", "loadbalancer"},
		{"Cisco device", "", "generic"}, // vendor name with no role word and no icon
	}
	for _, c := range cases {
		if got := For(c.typeName, c.basename); got.ID != c.wantID {
			t.Errorf("For(%q, %q) = %q, want %q", c.typeName, c.basename, got.ID, c.wantID)
		}
	}
}

func TestForFallbacks(t *testing.T) {
	if got := For("", ""); got != Generic {
		t.Errorf("For(empty) = %v, want Generic", got)
	}
	if got := For("Mystery Box 9000", ""); got != Generic {
		t.Errorf("For(unknown name) = %v, want Generic", got)
	}
	if got := For("unknown", ""); got != Generic {
		t.Errorf("For(unknown) = %v, want Generic", got)
	}
	if got := ForName("MikroTik router"); got.ID != "router" {
		t.Errorf("ForName(MikroTik router) = %q, want router", got.ID)
	}
	if got := ForName("Linux server (Net-SNMP)"); got.ID != "linux-server" {
		t.Errorf("ForName(Net-SNMP) = %q, want linux-server", got.ID)
	}
	if got := ForName("APC UPS"); got.ID != "ups" {
		t.Errorf("ForName(APC UPS) = %q, want ups", got.ID)
	}
	if got := ForName("Fortinet firewall"); got.ID != "firewall" {
		t.Errorf("ForName(Fortinet firewall) = %q, want firewall", got.ID)
	}
}

func TestForDeviceFallbackToPollerType(t *testing.T) {
	if got := ForDevice("", "", "switch"); got.ID != "switch" {
		t.Errorf("ForDevice poller switch = %q, want switch", got.ID)
	}
	if got := ForDevice("", "", ""); got != Generic {
		t.Errorf("ForDevice all empty = %v, want Generic", got)
	}
	if got := ForDevice("Router", "Router.png", "server"); got.ID != "router" {
		t.Errorf("ForDevice legacy wins = %q, want router", got.ID)
	}
}

func TestAccentsPresent(t *testing.T) {
	for key, ic := range byBasename {
		if ic.ID == "" || ic.Accent == "" {
			t.Errorf("basename %q maps to incomplete icon %+v", key, ic)
		}
	}
	for _, p := range namePatterns {
		if p.icon.ID == "" || p.icon.Accent == "" {
			t.Errorf("pattern %q maps to incomplete icon %+v", p.needle, p.icon)
		}
	}
}
