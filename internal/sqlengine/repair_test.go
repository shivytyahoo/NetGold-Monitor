package sqlengine

import (
	"path/filepath"
	"strings"
	"testing"
)

// ---- MSI source selection (workstream 3) ----

func TestChooseMSISource(t *testing.T) {
	cases := []struct {
		downloadOK, bundled bool
		want                msiSource
	}{
		{true, true, sourceDownload},  // a good download always wins
		{true, false, sourceDownload}, // download only (portable run)
		{false, true, sourceBundled},  // offline: bundled fallback
		{false, false, sourceNone},    // nothing available
	}
	for _, c := range cases {
		if got := chooseMSISource(c.downloadOK, c.bundled); got != c.want {
			t.Errorf("chooseMSISource(%v, %v) = %v, want %v", c.downloadOK, c.bundled, got, c.want)
		}
	}
}

// ---- repair planning allowlists ----

func TestLocalDBProductQualifies(t *testing.T) {
	yes := []string{
		"Microsoft SQL Server 2022 Express LocalDB",
		"Microsoft SQL Server 2019 Express LocalDB",
		"Microsoft SQL Server Local DB",
		"SqlLocalDB",
	}
	for _, n := range yes {
		if !LocalDBProductQualifies(n) {
			t.Errorf("%q must qualify as a LocalDB product", n)
		}
	}
	no := []string{
		"Microsoft SQL Server 2022 (64-bit)",
		"Microsoft SQL Server 2022 Database Engine Services",
		"Microsoft SQL Server 2005",
		"Some Network Monitor",
		"Microsoft SQL Server Compact 4.0 SP1 x64 ENU",
		"",
	}
	for _, n := range no {
		if LocalDBProductQualifies(n) {
			t.Errorf("%q must NEVER qualify — the repair only touches LocalDB products", n)
		}
	}
}

func TestQualifyingProducts(t *testing.T) {
	entries := []uninstallEntry{
		{DisplayName: "Microsoft SQL Server 2022 (64-bit)", ProductCode: "{AAAA}"},
		{DisplayName: "Microsoft SQL Server 2022 Express LocalDB", ProductCode: "{BBBB}"},
		{DisplayName: "Microsoft SQL Server 2019 Express LocalDB", ProductCode: "{CCCC}"},
		{DisplayName: "Some Other Program", ProductCode: "{DDDD}"},
	}
	got := qualifyingProducts(entries)
	if len(got) != 2 || got[0].ProductCode != "{BBBB}" || got[1].ProductCode != "{CCCC}" {
		t.Errorf("qualifying products wrong: %+v", got)
	}
}

func TestRepairRegistryAllowlist(t *testing.T) {
	// The complete allowlist: exactly the two LocalDB keys, one per
	// hive, and nothing else — in particular no key of a full SQL
	// Server installation.
	if len(repairRegistryKeys) != 2 {
		t.Fatalf("registry allowlist must have exactly 2 entries, got %d", len(repairRegistryKeys))
	}
	roots := map[string]bool{}
	for _, k := range repairRegistryKeys {
		if k.Path != `SOFTWARE\Microsoft\Microsoft SQL Server Local DB` {
			t.Errorf("unexpected registry key in allowlist: %s\\%s", k.Root, k.Path)
		}
		roots[k.Root] = true
	}
	if !roots["HKCU"] || !roots["HKLM"] {
		t.Errorf("allowlist must cover HKCU and HKLM: %v", roots)
	}
}

func TestQualifyingProgramFolders(t *testing.T) {
	roots := []string{`C:\Program Files`, `C:\Program Files (x86)`}
	got := qualifyingProgramFolders(roots)
	if len(got) != 2*len(localDBVersions) {
		t.Fatalf("want %d folders, got %d", 2*len(localDBVersions), len(got))
	}
	for _, p := range got {
		if !strings.HasSuffix(p, "LocalDB") {
			t.Errorf("every qualifying folder must be a LocalDB folder: %s", p)
		}
		if strings.Contains(p, "SQLEXPRESS") || strings.Contains(p, "MSSQL") {
			t.Errorf("a real SQL Server folder must never qualify: %s", p)
		}
	}
	want := filepath.Join(`C:\Program Files`, "Microsoft SQL Server", "160", "LocalDB")
	found := false
	for _, p := range got {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in the qualifying folders", want)
	}
	// Empty roots are skipped, not turned into relative paths.
	if got := qualifyingProgramFolders([]string{""}); len(got) != 0 {
		t.Errorf("empty root must yield no folders, got %v", got)
	}
}

func TestPerUserInstancesFolder(t *testing.T) {
	got := perUserInstancesFolder(`C:\Users\Test\AppData\Local`)
	want := filepath.Join(`C:\Users\Test\AppData\Local`, "Microsoft", "Microsoft SQL Server Local DB", "Instances")
	if got != want {
		t.Errorf("per-user instances folder: got %q want %q", got, want)
	}
	if got := perUserInstancesFolder(""); got != "" {
		t.Errorf("empty LOCALAPPDATA must yield empty, got %q", got)
	}
}

func TestUniqueRoots(t *testing.T) {
	got := uniqueRoots([]string{`C:\Program Files`, "", `c:\program files`, `C:\Program Files (x86)`, "  "})
	if len(got) != 2 || got[0] != `C:\Program Files` || got[1] != `C:\Program Files (x86)` {
		t.Errorf("uniqueRoots wrong: %v", got)
	}
}

func TestParseRepairResult(t *testing.T) {
	if ok, msg := parseRepairResult("OK\nThe SQL engine installation was repaired."); !ok || msg == "" {
		t.Errorf("OK result: ok=%v msg=%q", ok, msg)
	}
	if ok, msg := parseRepairResult("FAILED\nThe LocalDB installer failed (exit code 1603)."); ok || !strings.Contains(msg, "1603") {
		t.Errorf("FAILED result: ok=%v msg=%q", ok, msg)
	}
	if ok, _ := parseRepairResult(""); ok {
		t.Error("empty result must not be OK")
	}
	if ok, _ := parseRepairResult("GARBAGE"); ok {
		t.Error("unreadable result must not be OK")
	}
}
