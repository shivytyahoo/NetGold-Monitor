package store

import (
	"path/filepath"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	return st
}

func TestFlowBucketsAggregateAndTop(t *testing.T) {
	st := tempStore(t)
	// Same bucket key twice must accumulate.
	if err := st.AddFlow(60000, "10.0.0.254", "10.0.0.1", "10.0.0.2", 443, 6, 1000, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.AddFlow(60000, "10.0.0.254", "10.0.0.1", "10.0.0.2", 443, 6, 500, 5); err != nil {
		t.Fatal(err)
	}
	if err := st.AddFlow(60000, "10.0.0.254", "10.0.0.3", "10.0.0.2", 53, 17, 200, 2); err != nil {
		t.Fatal(err)
	}
	top, err := st.FlowTopN("src", 0, 99999, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Key != "10.0.0.1" || top[0].Bytes != 1500 || top[0].Packets != 15 {
		t.Errorf("top sources: %+v", top)
	}
	conv, _ := st.FlowTopN("conv", 0, 99999, 10)
	if len(conv) != 2 || conv[0].Key2 != "10.0.0.2" {
		t.Errorf("conversations: %+v", conv)
	}
	b, p, err := st.FlowTotals(0, 99999)
	if err != nil || b != 1700 || p != 17 {
		t.Errorf("totals: %d/%d err=%v", b, p, err)
	}
	series, _ := st.FlowSeries(0, 99999)
	if len(series) != 1 || series[0].Value != 1700 {
		t.Errorf("series: %+v", series)
	}
}

func TestSyslogStoreAndSearch(t *testing.T) {
	st := tempStore(t)
	_ = st.AddSyslog(SyslogEntry{SourceIP: "10.0.0.1", Hostname: "sw1", Facility: 16, Severity: 3, App: "kernel", Message: "link down on ge-0/0/1"})
	_ = st.AddSyslog(SyslogEntry{SourceIP: "10.0.0.2", Hostname: "sw2", Facility: 16, Severity: 6, App: "sshd", Message: "login accepted"})
	found, err := st.SearchSyslogs(SyslogQuery{Text: "link down", Severity: -1})
	if err != nil || len(found) != 1 || found[0].Hostname != "sw1" {
		t.Errorf("text search: %+v err=%v", found, err)
	}
	found, _ = st.SearchSyslogs(SyslogQuery{Severity: 6})
	if len(found) != 1 || found[0].App != "sshd" {
		t.Errorf("severity search: %+v", found)
	}
	counts, _ := st.SyslogSeverityCounts(0)
	if counts[3] != 1 || counts[6] != 1 {
		t.Errorf("counts: %v", counts)
	}
}

func TestDiscoveredLinkNormalisation(t *testing.T) {
	st := tempStore(t)
	// Insert "backwards" (B=9, A=3): the row must be stored normalised
	// (device_a < device_b) with interfaces swapped along.
	err := st.UpsertDiscoveredLink(DiscoveredLink{DeviceA: 9, IfA: "p9", DeviceB: 3, IfB: "p3", Via: "LLDP", Confidence: "high"})
	if err != nil {
		t.Fatal(err)
	}
	// The symmetric insert must merge into the same row.
	err = st.UpsertDiscoveredLink(DiscoveredLink{DeviceA: 3, IfA: "p3", DeviceB: 9, IfB: "p9", Via: "LLDP", Confidence: "high"})
	if err != nil {
		t.Fatal(err)
	}
	links, err := st.ListDiscoveredLinks()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].DeviceA != 3 || links[0].DeviceB != 9 || links[0].IfA != "p3" || links[0].IfB != "p9" {
		t.Errorf("links: %+v", links)
	}
}
