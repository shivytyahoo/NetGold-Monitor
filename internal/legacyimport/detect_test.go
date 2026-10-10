package legacyimport

import (
	"encoding/binary"
	"strings"
	"testing"
)

// A realistic SQL Browser response payload: two instances on one host.
const ssrpSamplePayload = "ServerName;TESTHOST;InstanceName;MSSQLSERVER;IsClustered;No;Version;17.0.5005.3;tcp;1433;np;\\\\TESTHOST\\pipe\\sql\\query;;" +
	"ServerName;TESTHOST;InstanceName;LEGACY;IsClustered;No;Version;15.0.2000.5;tcp;51433;np;\\\\TESTHOST\\pipe\\MSSQL$LEGACY\\sql\\query;;"

func ssrpDatagram(payload string) []byte {
	b := make([]byte, 3+len(payload))
	b[0] = 0x05
	binary.LittleEndian.PutUint16(b[1:3], uint16(len(payload)))
	copy(b[3:], payload)
	return b
}

func TestParseSSRPResponse(t *testing.T) {
	insts, err := ParseSSRPResponse(ssrpDatagram(ssrpSamplePayload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("want 2 instances, got %d: %+v", len(insts), insts)
	}
	if insts[0].ServerName != "TESTHOST" || insts[0].InstanceName != "MSSQLSERVER" || insts[0].TCPPort != 1433 {
		t.Errorf("instance 0 wrong: %+v", insts[0])
	}
	if insts[1].InstanceName != "LEGACY" || insts[1].TCPPort != 51433 {
		t.Errorf("instance 1 wrong: %+v", insts[1])
	}
	if insts[1].Version != "15.0.2000.5" {
		t.Errorf("version not parsed: %+v", insts[1])
	}
}

func TestParseSSRPResponseGarbage(t *testing.T) {
	for _, bad := range [][]byte{
		{},
		{0x05},
		{0x05, 0x01},
		{0x09, 0x00, 0x00}, // wrong response type
		[]byte("this is not an ssrp response at all"),
	} {
		insts, err := ParseSSRPResponse(bad)
		if err == nil && len(insts) > 0 {
			t.Errorf("garbage %q: got instances %+v without error", bad, insts)
		}
	}
	// Truncated length field: must not panic, yields what it can.
	if _, err := ParseSSRPResponse(ssrpDatagram("ServerName;H;InstanceName;X;tcp;1444;;")[:8]); err != nil {
		t.Logf("truncated datagram error (acceptable): %v", err)
	}
}

func TestParseSSRPDataEmpty(t *testing.T) {
	if got := ParseSSRPData(""); len(got) != 0 {
		t.Errorf("empty payload: want 0 instances, got %+v", got)
	}
	if got := ParseSSRPData(";;;;"); len(got) != 0 {
		t.Errorf("separator-only payload: want 0 instances, got %+v", got)
	}
}

func TestBuildCandidates(t *testing.T) {
	ssrp := []SSRPInstance{
		{ServerName: "TESTHOST", InstanceName: "MSSQLSERVER", TCPPort: 1433},
		{ServerName: "TESTHOST", InstanceName: "LEGACY", TCPPort: 51433},
	}
	cands := buildCandidates(nil, ssrp, nil)
	if len(cands) != 2 {
		t.Fatalf("ssrp only: want 2 candidates (no extra default — 1433 already covered), got %d: %+v", len(cands), cands)
	}
	if cands[0].Target.Port != 1433 || cands[1].Target.Port != 51433 {
		t.Errorf("ports wrong: %+v", cands)
	}

	// Registry only: LEGACY (instance form) + MSSQLSERVER (default form).
	cands = buildCandidates([]string{"LEGACY", "MSSQLSERVER"}, nil, nil)
	if len(cands) != 2 {
		t.Fatalf("registry only: want 2 candidates, got %d: %+v", len(cands), cands)
	}
	var sawInstance, sawDefault bool
	for _, c := range cands {
		if c.Target.Instance == "LEGACY" {
			sawInstance = true
		}
		if c.Target.Port == 1433 && c.Target.Instance == "" {
			sawDefault = true
		}
	}
	if !sawInstance || !sawDefault {
		t.Errorf("registry candidates wrong: %+v", cands)
	}

	// SSRP already reported LEGACY: registry must not duplicate it.
	cands = buildCandidates([]string{"LEGACY"}, ssrp, nil)
	n := 0
	for _, c := range cands {
		if strings.Contains(c.DisplayName, "LEGACY") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("LEGACY should appear exactly once, got %d in %+v", n, cands)
	}

	// Nothing at all: the default-instance candidate is still offered.
	cands = buildCandidates(nil, nil, nil)
	if len(cands) != 1 || cands[0].Target.Port != 1433 {
		t.Errorf("empty inputs: want the single default candidate, got %+v", cands)
	}

	// SSRP instance without a TCP port: target keeps the instance name so
	// the driver can resolve it through the Browser at connect time.
	cands = buildCandidates(nil, []SSRPInstance{{ServerName: "H", InstanceName: "DYN", TCPPort: 0}}, nil)
	found := false
	for _, c := range cands {
		if c.Target.Instance == "DYN" {
			found = true
		}
	}
	if !found {
		t.Errorf("dynamic-port SSRP instance lost: %+v", cands)
	}
}

func TestParseInstancePorts(t *testing.T) {
	names := map[string]string{
		"LEGACY":      "MSSQL.1",
		"STATIC":      "MSSQL.2",
		"DISABLED":    "MSSQL.3",
		"EMPTYCFG":    "MSSQL.4",
		"NOCFG":       "MSSQL.5",
		"MSSQLSERVER": "MSSQL.6",
		"PARTIAL":     "MSSQL.7",
	}
	ipAll := map[string]map[string]string{
		// Dynamic port recorded (the SQL 2005-era legacy case).
		"MSSQL.1": {"TcpPort": "", "TcpDynamicPorts": "51433", "TcpEnabled": "1"},
		// Static port wins over a dynamic value.
		"MSSQL.2": {"TcpPort": "1500", "TcpDynamicPorts": "51000"},
		// Explicit zeroes: TCP/IP disabled in effect.
		"MSSQL.3": {"TcpPort": "0", "TcpDynamicPorts": "0"},
		// Configuration exists but both values are empty: disabled.
		"MSSQL.4": {"TcpPort": "", "TcpDynamicPorts": ""},
		// MSSQL.5 deliberately absent: missing keys = no information.
		// Default instance with a static non-standard port.
		"MSSQL.6": {"TcpPort": "2433", "TcpDynamicPorts": ""},
		// Only TcpPort present, no TcpDynamicPorts key at all.
		"MSSQL.7": {"TcpPort": "1600"},
	}
	hints := ParseInstancePorts(names, ipAll)
	if len(hints) != len(names) {
		t.Fatalf("want %d hints, got %d: %+v", len(names), len(hints), hints)
	}
	byInst := map[string]InstancePortHint{}
	for _, h := range hints {
		byInst[h.Instance] = h
	}
	if h := byInst["LEGACY"]; h.Port != 51433 || !h.Dynamic || h.Disabled || h.ServiceID != "MSSQL.1" {
		t.Errorf("dynamic port: %+v", h)
	}
	if h := byInst["STATIC"]; h.Port != 1500 || h.Dynamic {
		t.Errorf("static port: %+v", h)
	}
	if h := byInst["DISABLED"]; !h.Disabled || h.Port != 0 {
		t.Errorf("explicit zero ports: %+v", h)
	}
	if h := byInst["EMPTYCFG"]; !h.Disabled {
		t.Errorf("empty port values: %+v", h)
	}
	if h := byInst["NOCFG"]; h.Port != 0 || h.Disabled {
		t.Errorf("missing IPAll keys must be 'no information', not disabled: %+v", h)
	}
	if h := byInst["MSSQLSERVER"]; h.Port != 2433 || h.Dynamic {
		t.Errorf("default instance static port: %+v", h)
	}
	if h := byInst["PARTIAL"]; h.Port != 1600 {
		t.Errorf("only TcpPort key present: %+v", h)
	}
	// Output is sorted by instance name (deterministic).
	for i := 1; i < len(hints); i++ {
		if hints[i-1].Instance > hints[i].Instance {
			t.Fatalf("hints not sorted: %+v", hints)
		}
	}
	if got := ParseInstancePorts(nil, nil); len(got) != 0 {
		t.Errorf("nil inputs: want no hints, got %+v", got)
	}
}

func TestBuildCandidatesRegistryPorts(t *testing.T) {
	hints := []InstancePortHint{
		{Instance: "LEGACY", ServiceID: "MSSQL.1", Port: 51433, Dynamic: true},
		{Instance: "OLDINST", ServiceID: "MSSQL.2", Disabled: true, Detail: "TCP/IP appears disabled for this instance (its registry configuration records no TCP port)."},
		{Instance: "PLAIN", ServiceID: "MSSQL.3"}, // no information
	}
	cands := buildCandidates([]string{"LEGACY", "OLDINST", "PLAIN"}, nil, hints)
	var portCand, skipCand, nameCand *Candidate
	for i := range cands {
		c := &cands[i]
		switch {
		case c.Target.Port == 51433 && c.Target.Instance == "":
			portCand = c
		case c.ProbeSkip:
			skipCand = c
		case c.Target.Instance == "PLAIN":
			nameCand = c
		}
	}
	if portCand == nil {
		t.Fatalf("registry port hint must produce a direct-port candidate: %+v", cands)
	}
	if portCand.Target.Server != "127.0.0.1" || portCand.Source != "registry-port" {
		t.Errorf("port candidate wrong: %+v", *portCand)
	}
	// The instance-name target for LEGACY must be gone (replaced by
	// the direct port target).
	for _, c := range cands {
		if c.Target.Instance == "LEGACY" {
			t.Errorf("LEGACY should only appear as a port candidate: %+v", cands)
		}
	}
	if skipCand == nil || skipCand.Target.Instance != "OLDINST" ||
		!strings.Contains(skipCand.Detail, "TCP/IP appears disabled for this instance") {
		t.Errorf("disabled hint must produce a ProbeSkip candidate with the detail: %+v", cands)
	}
	if nameCand == nil {
		t.Errorf("no-information hint must keep the instance-name candidate: %+v", cands)
	}

	// A registry port that SSRP also reported must not duplicate.
	ssrp := []SSRPInstance{{ServerName: "H", InstanceName: "LEGACY", TCPPort: 51433}}
	cands = buildCandidates([]string{"LEGACY"}, ssrp, hints[:1])
	n := 0
	for _, c := range cands {
		if c.Target.Port == 51433 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("same port from SSRP and registry must dedupe to one candidate, got %d: %+v", n, cands)
	}
}

func TestFindLegacyDB(t *testing.T) {
	sig := []string{"Device", "DeviceType", "MapView", "Annotation"}
	if got := legacyDBFromTables(
		[]string{"master", "LegacyMon", "tempdb"},
		map[string][]string{"master": {"spt_values"}, "LegacyMon": sig},
	); got != "LegacyMon" {
		t.Errorf("want LegacyMon, got %q", got)
	}
	// Signature matching is case-insensitive on table names, and a
	// renamed legacy database is still found.
	if got := legacyDBFromTables(
		[]string{"renameddb"},
		map[string][]string{"renameddb": {"device", "MAPVIEW"}},
	); got != "renameddb" {
		t.Errorf("want renameddb, got %q", got)
	}
	// A partial signature is not enough, and system databases are
	// never picked even if they somehow carried the tables.
	if got := legacyDBFromTables(
		[]string{"master", "otherdb"},
		map[string][]string{"master": sig, "otherdb": {"Device"}},
	); got != "" {
		t.Errorf("no legacy database: want empty, got %q", got)
	}
}
