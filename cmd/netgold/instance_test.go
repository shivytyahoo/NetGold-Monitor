package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchWindowsImageName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"netgold.exe", true},
		{"NetGold.EXE", true},
		{"NETGOLD.EXE", true},
		{"netgold (1).exe", true}, // browser duplicate download
		{"netgold (2).exe", true},
		{"netgold-old.exe", true},
		{"netgold.exe.tmp", false},
		{"netgold", false},       // no .exe suffix
		{"mynetgold.exe", false}, // must start with netgold
		{"goldnet.exe", false},
		{"sqlservr.exe", false},
		{"", false},
	}
	for _, c := range cases {
		if got := matchWindowsImageName(c.name); got != c.want {
			t.Errorf("matchWindowsImageName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMatchUnixExeName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"netgold", true},
		{"NetGold", true},
		{"netgold-darwin-amd64", true},
		{"netgold-darwin-arm64", true},
		{"netgold.old", true},
		{"netgold (1)", true},
		{"netgold_v2", true},
		{"netgoldx", false}, // a different program that merely shares a prefix
		{"mynetgold", false},
		{"goldnet", false},
		{"", false},
	}
	for _, c := range cases {
		if got := matchUnixExeName(c.name); got != c.want {
			t.Errorf("matchUnixExeName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDecideTakeover(t *testing.T) {
	const ours = "2.2.2"
	cases := []struct {
		name       string
		probe      probeResult
		noTakeover bool
		want       takeoverAction
	}{
		{"free port", probeResult{state: probeNothing}, false, actionServe},
		{"free port, no-takeover flag", probeResult{state: probeNothing}, true, actionServe},
		{"same version", probeResult{state: probeNetGold, version: ours}, false, actionExitAlreadyRunning},
		{"same version, no-takeover flag", probeResult{state: probeNetGold, version: ours}, true, actionExitAlreadyRunning},
		{"older version", probeResult{state: probeNetGold, version: "2.2.1"}, false, actionTakeover},
		{"newer version", probeResult{state: probeNetGold, version: "9.9.9"}, false, actionTakeover},
		{"older version, no-takeover flag", probeResult{state: probeNetGold, version: "2.2.1"}, true, actionExitBlocked},
		{"unknown occupant", probeResult{state: probeUnknown}, false, actionExitBlocked},
		{"unknown occupant, no-takeover flag", probeResult{state: probeUnknown}, true, actionExitBlocked},
	}
	for _, c := range cases {
		if got := decideTakeover(c.probe, ours, c.noTakeover); got != c.want {
			t.Errorf("%s: decideTakeover = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCanonicalPaths(t *testing.T) {
	base := filepath.Join("C:", "Users", "shiv", "AppData", "Local")
	if got, want := canonicalExeFor(base), filepath.Join(base, "NetGold", "netgold.exe"); got != want {
		t.Errorf("canonicalExeFor = %q, want %q", got, want)
	}
	if got, want := canonicalDataDirFor(base), filepath.Join(base, "NetGold", "data"); got != want {
		t.Errorf("canonicalDataDirFor = %q, want %q", got, want)
	}
	cfgDir := filepath.Join("/home", "shiv", ".config")
	if got, want := userConfigDataDirFor(cfgDir), filepath.Join(cfgDir, "NetGold", "data"); got != want {
		t.Errorf("userConfigDataDirFor = %q, want %q", got, want)
	}
}

func TestMigrationNeeded(t *testing.T) {
	cases := []struct {
		legacy, target, want bool
	}{
		{true, false, true},   // the one-time carry-over case
		{true, true, false},   // target already has data — never overwrite
		{false, false, false}, // nothing to carry
		{false, true, false},
	}
	for _, c := range cases {
		if got := migrationNeeded(c.legacy, c.target); got != c.want {
			t.Errorf("migrationNeeded(%v, %v) = %v, want %v", c.legacy, c.target, got, c.want)
		}
	}
}

func TestMigrateLegacyData(t *testing.T) {
	// Plant a legacy data folder where the test pretends the program
	// lives: migrateLegacyData looks next to os.Executable(), which
	// in tests is the test binary — so instead exercise the decision
	// and copy primitives directly.
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "netgold.db"), []byte("db-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !migrationNeeded(fileExists(filepath.Join(src, "netgold.db")), fileExists(filepath.Join(dst, "netgold.db"))) {
		t.Fatal("expected migration to be needed")
	}
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "netgold.db"))
	if err != nil || string(got) != "db-bytes" {
		t.Fatalf("copied db = %q, %v", got, err)
	}
	if migrationNeeded(fileExists(filepath.Join(src, "netgold.db")), fileExists(filepath.Join(dst, "netgold.db"))) {
		t.Fatal("migration must not repeat once the target has a database")
	}
}

func TestPIDFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readPIDFile(dir); ok {
		t.Fatal("empty dir must not yield a PID")
	}
	writePIDFile(dir)
	pid, ok := readPIDFile(dir)
	if !ok || pid != os.Getpid() {
		t.Fatalf("readPIDFile = %d, %v; want %d, true", pid, ok, os.Getpid())
	}
	removePIDFile(dir)
	if _, ok := readPIDFile(dir); ok {
		t.Fatal("pidfile must be removed")
	}
	// Garbage content parses as "no pid", never a wrong one.
	if err := os.WriteFile(filepath.Join(dir, pidFileName), []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPIDFile(dir); ok {
		t.Fatal("garbage pidfile must not yield a PID")
	}
}

func TestPortOf(t *testing.T) {
	cases := []struct {
		addr string
		want string
		ok   bool
	}{
		{":8080", "8080", true},
		{"127.0.0.1:8080", "8080", true},
		{"localhost:9090", "9090", true},
		{":0", "", false},
		{":99999", "", false},
		{"8080", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := portOf(c.addr)
		if got != c.want || ok != c.ok {
			t.Errorf("portOf(%q) = %q, %v; want %q, %v", c.addr, got, ok, c.want, c.ok)
		}
	}
}

// The probe must read a version out of a pre-v2.2.2 index page (the
// fallback that lets a new copy replace a 2.2.1 one).
func TestFooterVersionRe(t *testing.T) {
	page := []byte(`<div class="sidefoot"><div>NetGold Monitor v2.2.1</div></div>`)
	m := footerVersionRe.FindSubmatch(page)
	if m == nil || string(m[1]) != "2.2.1" {
		t.Fatalf("footerVersionRe on v2.2.1 page = %v", m)
	}
	if m := footerVersionRe.FindSubmatch([]byte("some other server")); m != nil {
		t.Fatalf("footerVersionRe must not match foreign pages, got %v", m)
	}
}
