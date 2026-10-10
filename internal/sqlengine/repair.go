package sqlengine

// Engine repair (v3.0): the residue cleanup that used to live in a
// standalone rescue script, ported into the product.
//
// When a plain LocalDB install fails (the classic case is exit code
// 1603) or an installed LocalDB turns out to be broken, the usual
// cause is residue from an earlier, interrupted SQL Server setup:
// half-registered LocalDB products, stale per-user instance files and
// leftover registry keys. The repair removes exactly those LocalDB
// leftovers — and nothing else — then installs the engine again and
// recreates the NetGold instance.
//
// Safety rule, encoded in the planning functions below and unit
// tested: the repair touches LOCALDB-NAMED items only. It never
// stops, uninstalls or deletes a real SQL Server instance, service or
// database file (a real named instance, for example, is never matched:
// its product names, folders and registry keys do not contain
// "LocalDB"). The allowlists here are the single place that
// decides what qualifies; the Windows executor in repair_windows.go
// only ever acts on what these functions return.
//
// The repair itself runs in a hidden, elevated copy of this program
// (netgold -engine-repair): one Windows administrator (UAC) consent
// covers the whole cleanup + reinstall. The elevated child reports
// progress and its result through files in the data directory, which
// the setup job polls and mirrors into its status.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- MSI source selection (workstream 3) ----

// msiSource identifies where the LocalDB installer comes from.
type msiSource int

const (
	sourceNone msiSource = iota
	// sourceDownload: the pinned official installer, downloaded from
	// Microsoft (and Authenticode-verified before use, as always).
	sourceDownload
	// sourceBundled: the SqlLocalDB.msi copy the Windows installer
	// placed in the engine folder next to the program — the offline
	// fallback, also Authenticode-verified before use.
	sourceBundled
)

// chooseMSISource is the pure source-selection rule: a successful
// official download always wins; when the download failed for any
// reason, the bundled copy is used if one exists; otherwise there is
// no source. The downloaded and bundled files are the same pinned
// installer, and both pass the same signature gate before running.
func chooseMSISource(downloadOK, bundledExists bool) msiSource {
	if downloadOK {
		return sourceDownload
	}
	if bundledExists {
		return sourceBundled
	}
	return sourceNone
}

// bundledMSIPath is where the Windows installer places its bundled
// SqlLocalDB.msi: an "engine" folder next to the program file. A
// portable run simply has no such folder, and the download remains
// the only source (the pre-v3.0 behaviour).
func bundledMSIPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "engine", "SqlLocalDB.msi")
}

func fileExistsNonEmpty(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// ---- repair planning (pure, unit-tested) ----

// LocalDBProductQualifies decides whether a registered product (from
// the Uninstall registry keys) is a LocalDB product the repair may
// uninstall. The match is on the product's display name only, after
// removing spaces, so "Local DB" spellings count too. Full SQL Server
// products ("Microsoft SQL Server 2022 (64-bit)", database engine
// services, etc.) never contain the LocalDB name and never qualify.
func LocalDBProductQualifies(displayName string) bool {
	n := strings.ToLower(strings.ReplaceAll(displayName, " ", ""))
	return strings.Contains(n, "localdb")
}

// repairRegistryKeys is the complete allowlist of registry keys the
// repair backs up and then deletes: the LocalDB keys, per-user and
// machine-wide. No other key is ever touched.
var repairRegistryKeys = []struct {
	Root string // "HKCU" or "HKLM"
	Path string
}{
	{"HKCU", `SOFTWARE\Microsoft\Microsoft SQL Server Local DB`},
	{"HKLM", `SOFTWARE\Microsoft\Microsoft SQL Server Local DB`},
}

// localDBVersions lists the SQL Server version folders that can hold
// a LocalDB program folder (170 = 2025 … 110 = 2012).
var localDBVersions = []string{"170", "160", "150", "140", "130", "120", "110"}

// qualifyingProgramFolders returns every leftover LocalDB program
// folder the repair may delete: <root>\Microsoft SQL Server\<ver>\
// LocalDB for each Program Files root and each known version. Roots
// must include the real 64-bit Program Files folder (ProgramW6432)
// whenever the process view is 32-bit — the v2 rescue script's blind
// spot was trusting a single %ProgramFiles% that pointed at the
// (x86) folder, so it "cleaned" the wrong tree and reported the
// freshly installed tool as missing.
func qualifyingProgramFolders(roots []string) []string {
	var out []string
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, v := range localDBVersions {
			out = append(out, filepath.Join(root, "Microsoft SQL Server", v, "LocalDB"))
		}
	}
	return out
}

// perUserInstancesFolder is the per-user LocalDB instance-files
// folder under %LOCALAPPDATA% that the repair removes.
func perUserInstancesFolder(localAppData string) string {
	if localAppData == "" {
		return ""
	}
	return filepath.Join(localAppData, "Microsoft", "Microsoft SQL Server Local DB", "Instances")
}

// uninstallEntry is one registered product from the Uninstall
// registry keys: its display name and its product code (the subkey
// name, an MSI GUID).
type uninstallEntry struct {
	DisplayName string
	ProductCode string
}

// qualifyingProducts filters registered products down to the ones
// the repair may uninstall: LocalDB products only (see
// LocalDBProductQualifies). Pure function.
func qualifyingProducts(entries []uninstallEntry) []uninstallEntry {
	var out []uninstallEntry
	for _, e := range entries {
		if LocalDBProductQualifies(e.DisplayName) {
			out = append(out, e)
		}
	}
	return out
}

// uniqueRoots de-duplicates a list of folder roots (Program Files
// candidates), dropping empties and case-insensitive repeats while
// preserving order. Pure function.
func uniqueRoots(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		k := strings.ToLower(filepath.Clean(r))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// ---- repair child contract ----

// Repair file names inside the data directory: the elevated child
// appends human-readable progress lines to RepairProgressFile and
// writes its final outcome to RepairResultFile (first line "OK" or
// "FAILED", followed by the message). The parent job polls both.
const (
	RepairProgressFile = "engine-repair-progress.txt"
	RepairResultFile   = "engine-repair-result.txt"
)

// parseRepairResult interprets the child's result file content.
// Pure function.
func parseRepairResult(content string) (ok bool, message string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, "the repair program produced no result"
	}
	first, rest, _ := strings.Cut(content, "\n")
	switch strings.TrimSpace(first) {
	case "OK":
		return true, strings.TrimSpace(rest)
	case "FAILED":
		return false, strings.TrimSpace(rest)
	}
	return false, "the repair program produced an unreadable result"
}

// platRunRepair runs the whole repair (elevated) for the MSI at
// msiPath, reporting progress lines as they happen. Implemented on
// Windows (repair_windows.go); elsewhere it reports unsupported.
var platRunRepair = func(msiPath, dataDir string, progress func(string)) error {
	return errUnsupported()
}

// RunRepairChild is the elevated child's entry point, called by the
// program's main function when it is relaunched with -engine-repair.
// It performs the repair steps itself (this process already has
// administrator rights) and reports through the data-dir files.
// Implemented on Windows; other platforms return an error.
var RunRepairChild = func(dataDir, msiPath string) error {
	return fmt.Errorf("engine repair is available on Windows only")
}
