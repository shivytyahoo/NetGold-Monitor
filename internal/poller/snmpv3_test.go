package poller

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMP v3 support is verified at the parameter-mapping level: no live
// SNMP v3 device exists in the build environment, so these tests assert
// that NetGold's credential settings map onto the correct gosnmp USM
// parameters (the poll itself runs through the same FetchAll code path
// as v2c once the client is built).
func TestV3ParamMappingAuthPriv(t *testing.T) {
	g := &gosnmp.GoSNMP{}
	applyCreds(g, SnmpCreds{
		Version: "v3", SecName: "nocuser",
		AuthProto: "SHA", AuthPass: "auth-secret",
		PrivProto: "AES", PrivPass: "priv-secret",
		SecLevel: "authPriv",
	})
	if g.Version != gosnmp.Version3 {
		t.Fatalf("version = %v, want Version3", g.Version)
	}
	if g.SecurityModel != gosnmp.UserSecurityModel {
		t.Fatalf("security model = %v, want UserSecurityModel", g.SecurityModel)
	}
	if g.MsgFlags != gosnmp.AuthPriv {
		t.Fatalf("msg flags = %v, want AuthPriv", g.MsgFlags)
	}
	usm, ok := g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok {
		t.Fatalf("security parameters type = %T", g.SecurityParameters)
	}
	if usm.UserName != "nocuser" {
		t.Errorf("user name = %q", usm.UserName)
	}
	if usm.AuthenticationProtocol != gosnmp.SHA {
		t.Errorf("auth protocol = %v, want SHA", usm.AuthenticationProtocol)
	}
	if usm.AuthenticationPassphrase != "auth-secret" {
		t.Errorf("auth passphrase not mapped")
	}
	if usm.PrivacyProtocol != gosnmp.AES {
		t.Errorf("privacy protocol = %v, want AES", usm.PrivacyProtocol)
	}
	if usm.PrivacyPassphrase != "priv-secret" {
		t.Errorf("privacy passphrase not mapped")
	}
	if g.Timeout != 2*time.Second || g.Retries != 1 {
		t.Errorf("probe defaults = %v/%d, want 2s/1 (the legacy system)", g.Timeout, g.Retries)
	}
}

func TestV3ParamMappingLevels(t *testing.T) {
	cases := []struct {
		name  string
		creds SnmpCreds
		flags gosnmp.SnmpV3MsgFlags
		auth  gosnmp.SnmpV3AuthProtocol
		priv  gosnmp.SnmpV3PrivProtocol
	}{
		{"authNoPriv MD5", SnmpCreds{Version: "v3", SecName: "u", AuthProto: "MD5", AuthPass: "p", SecLevel: "authNoPriv"},
			gosnmp.AuthNoPriv, gosnmp.MD5, gosnmp.NoPriv},
		{"noAuthNoPriv", SnmpCreds{Version: "v3", SecName: "u", SecLevel: "noAuthNoPriv"},
			gosnmp.NoAuthNoPriv, gosnmp.NoAuth, gosnmp.NoPriv},
		{"authPriv DES", SnmpCreds{Version: "v3", SecName: "u", AuthProto: "SHA", AuthPass: "p", PrivProto: "DES", PrivPass: "q", SecLevel: "authPriv"},
			gosnmp.AuthPriv, gosnmp.SHA, gosnmp.DES},
		{"authPriv without auth protocol falls back", SnmpCreds{Version: "v3", SecName: "u", SecLevel: "authPriv"},
			gosnmp.NoAuthNoPriv, gosnmp.NoAuth, gosnmp.NoPriv},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &gosnmp.GoSNMP{}
			applyCreds(g, c.creds)
			if g.MsgFlags != c.flags {
				t.Errorf("msg flags = %v, want %v", g.MsgFlags, c.flags)
			}
			usm := g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
			if usm.AuthenticationProtocol != c.auth {
				t.Errorf("auth = %v, want %v", usm.AuthenticationProtocol, c.auth)
			}
			if usm.PrivacyProtocol != c.priv {
				t.Errorf("priv = %v, want %v", usm.PrivacyProtocol, c.priv)
			}
		})
	}
}

func TestV2cMappingUnchanged(t *testing.T) {
	g := &gosnmp.GoSNMP{}
	applyCreds(g, SnmpCreds{Version: "v2c", Community: "private"})
	if g.Version != gosnmp.Version2c || g.Community != "private" {
		t.Fatalf("v2c mapping: version=%v community=%q", g.Version, g.Community)
	}
	if g.SecurityParameters != nil {
		t.Fatalf("v2c must not set USM parameters")
	}
}

func TestClassifySysObjectID(t *testing.T) {
	cases := map[string]string{
		"1.3.6.1.4.1.9.1.1041":    "Cisco device",
		".1.3.6.1.4.1.318.1.3.4":  "APC UPS",
		"1.3.6.1.4.1.8072.3.2.10": "Linux server (Net-SNMP)",
		"1.3.6.1.4.1.14988.1":     "MikroTik router",
		"1.3.6.1.4.1.99999.1":     "",
		"":                        "",
	}
	for oid, want := range cases {
		if got := ClassifySysObjectID(oid); got != want {
			t.Errorf("ClassifySysObjectID(%q) = %q, want %q", oid, got, want)
		}
	}
}
