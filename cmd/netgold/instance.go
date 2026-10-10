package main

// Single-instance takeover, canonical install paths and data
// migration (v2.2.2). Shiv's requirement: "when I run the newly
// downloaded exe, it should remove everything old and run only the
// new one." The platform halves (process sweep, Windows service
// check, canonical self-install) live in instance_<platform>.go;
// everything that can be decided or parsed platform-free lives here
// and is unit-tested.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"netgold/internal/web"
)

// ---------- version probe ----------

// probeState is what the startup probe found on our listen port.
type probeState int

const (
	probeNothing probeState = iota // port is free
	probeNetGold                   // a NetGold Monitor answers; version known
	probeUnknown                   // something answers, but it is not NetGold
)

type probeResult struct {
	state   probeState
	version string // when state == probeNetGold
}

// footerVersionRe extracts the version from a served index page. It is
// the fallback identification for NetGold builds older than v2.2.2,
// which have no /api/version endpoint but do print "NetGold Monitor
// vX.Y.Z" in the sidebar footer of the page they serve.
var footerVersionRe = regexp.MustCompile(`NetGold Monitor v([0-9]+\.[0-9]+\.[0-9]+)`)

// probePort asks whatever is listening on 127.0.0.1:port whether it
// is a NetGold Monitor, and if so, which version.
func probePort(port string) probeResult {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	base := "http://127.0.0.1:" + port
	if resp, err := client.Get(base + "/api/version"); err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var v struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(body, &v) == nil && v.Version != "" {
				return probeResult{state: probeNetGold, version: v.Version}
			}
		}
	}
	// Fallback for pre-v2.2.2 builds: their index page names the
	// version in the sidebar footer.
	if resp, err := client.Get(base + "/"); err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if m := footerVersionRe.FindSubmatch(body); m != nil {
				return probeResult{state: probeNetGold, version: string(m[1])}
			}
			if strings.Contains(string(body), "NetGold Monitor") {
				return probeResult{state: probeUnknown}
			}
			return probeResult{state: probeUnknown}
		}
		return probeResult{state: probeUnknown}
	}
	return probeResult{state: probeNothing}
}

// portOf extracts the TCP port from a listen address (":8080",
// "127.0.0.1:8080", "localhost:8080"). ok=false means the address
// has no usable port and the probe must be skipped.
func portOf(addr string) (string, bool) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "", false
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return "", false
	}
	return port, true
}

// ---------- takeover decision (pure) ----------

type takeoverAction int

const (
	actionServe takeoverAction = iota
	actionExitAlreadyRunning
	actionTakeover
	actionExitBlocked
)

// decideTakeover is the pure decision table behind ensureSingleInstance.
func decideTakeover(p probeResult, ourVersion string, noTakeover bool) takeoverAction {
	switch p.state {
	case probeNothing:
		return actionServe
	case probeUnknown:
		return actionExitBlocked
	case probeNetGold:
		if p.version == ourVersion {
			return actionExitAlreadyRunning
		}
		if noTakeover {
			return actionExitBlocked
		}
		return actionTakeover
	}
	return actionServe
}

// ensureSingleInstance probes the listen port before anything binds
// it. serve=true means this process should go on and serve; when
// serve=false the situation has been explained on the console and
// the process should exit with exitCode (0 for a friendly no-op —
// the same version is already running, or a service holds the port
// and updating it is a deliberate act; 1 when this copy was blocked).
// Shiv's rule: a newly started copy replaces whatever older NetGold
// is running — same version means "already running", a different
// version is terminated and replaced.
func ensureSingleInstance(cfg config) (serve bool, exitCode int) {
	port, ok := portOf(cfg.addr)
	if !ok {
		return true, 0
	}
	probe := probePort(port)
	switch decideTakeover(probe, web.Version, cfg.noTakeover) {
	case actionServe:
		return true, 0
	case actionExitAlreadyRunning:
		fmt.Printf("NetGold Monitor v%s is already running — opening the existing one: http://localhost:%s\n", web.Version, port)
		fmt.Println("Close that copy first if you want to restart it.")
		return false, 0
	case actionExitBlocked:
		if probe.state == probeNetGold {
			fmt.Printf("NetGold Monitor v%s is already running on port %s, and -no-takeover was given, so this copy will not replace it.\n", probe.version, port)
		} else {
			fmt.Printf("Port %s is already in use by a program that is not NetGold Monitor, so NetGold cannot start.\n", port)
			fmt.Println("Close that program, or start NetGold on another port, for example: netgold -addr :8081")
		}
		return false, 1
	case actionTakeover:
		// On Windows the port may belong to the NetGoldMonitor
		// service; a service is never killed from here.
		if serviceHoldsPort() {
			fmt.Println("A different NetGold Monitor version is running as the NetGoldMonitor Windows service.")
			fmt.Println("This copy will not stop a service. To update it, run these two commands in an")
			fmt.Println("Administrator terminal after closing this window:")
			fmt.Println("  netgold.exe -service stop")
			fmt.Println("  netgold.exe -service uninstall")
			fmt.Println("Then start this copy normally — or reinstall the service from it with:")
			fmt.Println("  netgold.exe -service install")
			return false, 0
		}
		fmt.Printf("Replacing the running NetGold Monitor v%s with v%s…\n", probe.version, web.Version)
		pids := otherNetGoldPIDs(cfg.dataDir)
		if len(pids) == 0 {
			fmt.Println("Could not identify the running copy's process, so it cannot be replaced automatically.")
			fmt.Println("Close the old NetGold Monitor window, then start this copy again.")
			return false, 1
		}
		killProcesses(pids)
		if !waitPortFree(port, 4*time.Second) {
			forceKillProcesses(pids)
			if !waitPortFree(port, 6*time.Second) {
				fmt.Println("The old copy did not stop in time, so this copy cannot start yet.")
				fmt.Println("Close the old NetGold Monitor window, then start this copy again.")
				return false, 1
			}
		}
		fmt.Printf("Replaced the running NetGold Monitor v%s with v%s.\n", probe.version, web.Version)
		return true, 0
	}
	return true, 0
}

// waitPortFree polls until nothing accepts TCP connections on the
// port, or the timeout passes.
func waitPortFree(port string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 300*time.Millisecond)
		if err != nil {
			return true
		}
		conn.Close()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ---------- pidfile (data dir) ----------

const pidFileName = "netgold.pid"

// writePIDFile records this process in the data directory so a later
// copy can find (and, on platforms without a process table scan,
// replace) it. Best effort: failures are silent.
func writePIDFile(dataDir string) {
	_ = os.WriteFile(filepath.Join(dataDir, pidFileName), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// removePIDFile deletes our pidfile if it still names this process.
func removePIDFile(dataDir string) {
	if pid, ok := readPIDFile(dataDir); ok && pid == os.Getpid() {
		_ = os.Remove(filepath.Join(dataDir, pidFileName))
	}
}

// readPIDFile parses the recorded PID; ok=false for a missing or
// unreadable file.
func readPIDFile(dataDir string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(dataDir, pidFileName))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// ---------- process-name matching (pure) ----------

// matchWindowsImageName reports whether a Windows image name belongs
// to a NetGold program file: the name starts with "netgold" and ends
// with ".exe" (covers netgold.exe and browser-renamed duplicates
// such as "netgold (1).exe").
func matchWindowsImageName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "netgold") && strings.HasSuffix(n, ".exe")
}

// matchUnixExeName reports whether a Unix executable base name
// belongs to a NetGold program file: "netgold", or "netgold" followed
// by a separator (netgold-darwin-amd64, netgold.old, "netgold (1)").
func matchUnixExeName(name string) bool {
	n := strings.ToLower(name)
	if n == "netgold" {
		return true
	}
	if !strings.HasPrefix(n, "netgold") || len(n) == len("netgold") {
		return false
	}
	switch n[len("netgold")] {
	case '-', '.', ' ', '_':
		return true
	}
	return false
}

// ---------- paths: canonical install + default data dir (pure) ----------

// pathUnderAnyRoot reports whether path lies inside one of the
// given root folders (case-insensitive, separator-aware). Pure
// function. Used on Windows to recognise a copy installed into
// Program Files by the NetGold installer: such a copy is already
// properly installed and must not be relocated to the per-user
// canonical location (its bundled engine folder sits beside it).
func pathUnderAnyRoot(path string, roots []string) bool {
	p := strings.ToLower(filepath.Clean(path))
	for _, r := range roots {
		if r == "" {
			continue
		}
		root := strings.ToLower(filepath.Clean(r))
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// canonicalExeFor is the one true program location on Windows:
// <base>\NetGold\netgold.exe where base is %LOCALAPPDATA%.
func canonicalExeFor(localAppData string) string {
	return filepath.Join(localAppData, "NetGold", "netgold.exe")
}

// canonicalDataDirFor is the matching data directory on Windows.
func canonicalDataDirFor(localAppData string) string {
	return filepath.Join(localAppData, "NetGold", "data")
}

// userConfigDataDirFor is the default data directory off Windows:
// <user config dir>/NetGold/data.
func userConfigDataDirFor(configDir string) string {
	return filepath.Join(configDir, "NetGold", "data")
}

// defaultDataDir resolves the data directory for this platform when
// the user did not pass -data.
func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return canonicalDataDirFor(base)
		}
		if cache, err := os.UserCacheDir(); err == nil && cache != "" {
			// UserCacheDir on Windows is %LOCALAPPDATA% itself.
			return canonicalDataDirFor(cache)
		}
	}
	if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
		return userConfigDataDirFor(cfgDir)
	}
	return "./data"
}

// ---------- one-time data migration (pure decision + copy) ----------

// migrationNeeded decides the one-time carry-over: the legacy data
// folder next to the launched program holds a database, and the
// target data folder does not have one yet.
func migrationNeeded(legacyDBExists, targetDBExists bool) bool {
	return legacyDBExists && !targetDBExists
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// migrateLegacyData copies a data folder left next to the program
// file (the pre-v2.2.2 default was ./data beside wherever the exe
// happened to be) into the now-canonical data directory. It runs
// only when -data was not given explicitly. Returns true when a
// carry-over happened.
func migrateLegacyData(targetDir string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	legacyDir := filepath.Join(filepath.Dir(exe), "data")
	legacyAbs, err1 := filepath.Abs(legacyDir)
	targetAbs, err2 := filepath.Abs(targetDir)
	if err1 != nil || err2 != nil || legacyAbs == targetAbs {
		return false
	}
	if !migrationNeeded(
		fileExists(filepath.Join(legacyDir, "netgold.db")),
		fileExists(filepath.Join(targetDir, "netgold.db")),
	) {
		return false
	}
	if err := copyDir(legacyDir, targetDir); err != nil {
		fmt.Printf("Note: your existing data in %s could not be carried over (%v). NetGold will start with a fresh database.\n", legacyDir, err)
		return false
	}
	fmt.Printf("Your existing data was carried over from %s.\n", legacyDir)
	return true
}

// copyDir copies every file of src into dst (created as needed),
// preserving one level of subdirectories used by the store.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
