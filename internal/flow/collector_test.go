package flow

import (
	"path/filepath"
	"testing"

	"netgold/internal/store"
)

// The collector's socket layer cannot run in every test environment, so
// this test drives handle() directly with the same fixture packets the
// decoder tests use and verifies the full decode -> aggregate -> store
// path, plus the recent-flows ring.
func TestCollectorAggregatesFixtures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	c := NewCollector(st, 0, 0)

	c.handle(buildV5(), "10.9.9.9", true)
	c.handle(buildSFlow(), "10.9.9.9", false)

	// v5: 64000 + 210 bytes; sFlow: 1500 bytes (rate 1).
	b, p, err := st.FlowTotals(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if b != 65710 {
		t.Errorf("total bytes = %d, want 65710", b)
	}
	if p != 104 { // 100 + 3 + 1
		t.Errorf("total packets = %d, want 104", p)
	}
	top, err := st.FlowTopN("dst", 0, 1<<62, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Key != "10.0.0.2" || top[0].Bytes != 64000 {
		t.Errorf("top destinations: %+v", top)
	}
	recent := c.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("recent ring: %d entries, want 3", len(recent))
	}
	if recent[0].Dst != "172.16.0.20" { // newest first: the sFlow record
		t.Errorf("newest recent flow: %+v", recent[0])
	}
	pkts, recs, bad, counters := c.Stats()
	if pkts != 2 || recs != 3 || bad != 0 || counters != 1 {
		t.Errorf("stats: packets=%d records=%d bad=%d counters=%d", pkts, recs, bad, counters)
	}
}
