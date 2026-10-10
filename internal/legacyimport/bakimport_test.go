package legacyimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTempDBNameValidation(t *testing.T) {
	name := TempDBName(time.Unix(1700000000, 123456789))
	if !strings.HasPrefix(name, "NetGoldImport_") {
		t.Fatalf("generated name lacks prefix: %s", name)
	}
	if !ValidTempDBName(name) {
		t.Fatalf("generated name must validate: %s", name)
	}
	for _, bad := range []string{
		"", "SomeDB", "master", "NetGoldImport_", "NetGoldImport_abc",
		"NetGoldImport_123; DROP DATABASE SomeDB", "NetGoldImport_12 3",
		"netgoldimport_123", "NetGoldImport_123]", "NetGoldImport_-1",
	} {
		if ValidTempDBName(bad) {
			t.Errorf("name %q must NOT validate — the drop guard depends on it", bad)
		}
	}
	// Two calls at different times produce different names.
	if TempDBName(time.Unix(1700000000, 1)) == TempDBName(time.Unix(1700000000, 2)) {
		t.Error("temp names must be unique per call")
	}
}

func TestValidateBakPath(t *testing.T) {
	// Empty / relative / wrong extension: rejected before any file check.
	for _, p := range []string{"", "   ", "backup.bak", "folder/backup.bak", "/tmp/x.zip", "/tmp/x.bak.txt"} {
		if err := ValidateBakPath(p); err == nil {
			t.Errorf("path %q: want error, got nil", p)
		}
	}
	// Absolute .bak that does not exist: a clear not-found error.
	missing := filepath.Join(t.TempDir(), "nope.bak")
	err := ValidateBakPath(missing)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing file: want a not-found error, got %v", err)
	}
	// A real .bak file validates; the extension check is case-insensitive.
	real := filepath.Join(t.TempDir(), "legacy.BAK")
	if err := os.WriteFile(real, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBakPath(real); err != nil {
		t.Errorf("real .BAK file should validate: %v", err)
	}
	// A directory named *.bak is not a backup file.
	dir := filepath.Join(t.TempDir(), "dir.bak")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBakPath(dir); err == nil {
		t.Error("directory named .bak: want error, got nil")
	}
}

func TestSplitDir(t *testing.T) {
	dir, sep, err := splitDir(`D:\MSSQL\Data\legacy.mdf`)
	if err != nil || dir != `D:\MSSQL\Data` || sep != `\` {
		t.Errorf("windows path: got dir=%q sep=%q err=%v", dir, sep, err)
	}
	dir, sep, err = splitDir("/var/opt/mssql/data/master.mdf")
	if err != nil || dir != "/var/opt/mssql/data" || sep != "/" {
		t.Errorf("linux path: got dir=%q sep=%q err=%v", dir, sep, err)
	}
	if got := joinServerPath(`D:\MSSQL\Data\`, `\`, "NetGoldImport_1.mdf"); got != `D:\MSSQL\Data\NetGoldImport_1.mdf` {
		t.Errorf("join windows: %q", got)
	}
	if got := joinServerPath("/var/opt/mssql/data/", "/", "NetGoldImport_1.mdf"); got != "/var/opt/mssql/data/NetGoldImport_1.mdf" {
		t.Errorf("join linux: %q", got)
	}
}

func TestSQLLiteralEscapesQuotes(t *testing.T) {
	if got := sqlLiteral(`C:\it's.bak`); got != `N'C:\it''s.bak'` {
		t.Errorf("literal not escaped: %s", got)
	}
	if got := bracket("NetGoldImport_123"); got != "[NetGoldImport_123]" {
		t.Errorf("bracket: %s", got)
	}
}
