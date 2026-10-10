package legacyimport

// Import from a .bak backup file (v1.3).
//
// A .bak file is a SQL Server backup: only a SQL Server engine can open
// it. NetGold therefore borrows a SQL Server instance that is already
// present (normally the one the legacy system itself installed on the same
// machine): it restores the backup into a short-lived temporary
// database, runs the normal importer from there, and always drops the
// temporary database again. Nothing else is ever dropped — the drop
// path is guarded by the strict temporary-name check below.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"netgold/internal/store"
)

// ValidateBakPath checks a user-supplied backup path: it must be an
// absolute path, end in .bak, and exist on this machine.
func ValidateBakPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("please enter the full path of the backup (.bak) file")
	}
	if !strings.EqualFold(filepath.Ext(path), ".bak") {
		return fmt.Errorf("the file must be a SQL Server backup file ending in .bak")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("please enter the full (absolute) path of the backup file, for example C:\\Backups\\legacy.bak")
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup file not found: %s — the path must point to a file on the computer running NetGold", path)
		}
		return fmt.Errorf("cannot read the backup file: %v", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("that path is a folder, not a backup file: %s", path)
	}
	return nil
}

// ValidateBakPathRemote validates a backup path for the manual server
// override (FIX 3). The file then lives on the SQL Server computer (or
// on a share that server can read), which is not necessarily the
// machine NetGold runs on, so only the form is checked here: non-empty,
// a .bak extension, and absolute in Windows (drive-letter or UNC) or
// POSIX form. The SQL Server itself reports a clear error if the file
// does not exist on its side.
func ValidateBakPathRemote(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("please enter the full path of the backup (.bak) file")
	}
	if !strings.EqualFold(filepath.Ext(path), ".bak") {
		return fmt.Errorf("the file must be a SQL Server backup file ending in .bak")
	}
	if !filepath.IsAbs(path) && !isWindowsAbsPath(path) {
		return fmt.Errorf("please enter the full (absolute) path of the backup file on the SQL Server computer, for example C:\\Backups\\legacy.bak")
	}
	return nil
}

// isWindowsAbsPath reports whether p is an absolute Windows path
// (C:\... or C:/..., or a UNC path \\server\share\...), on any OS.
func isWindowsAbsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true // UNC
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// TempDBName generates the temporary database name for one .bak import.
func TempDBName(t time.Time) string {
	return fmt.Sprintf("NetGoldImport_%d", t.UnixNano())
}

// ValidTempDBName reports whether name matches the generated temporary
// pattern exactly. The drop-database code path refuses anything else.
func ValidTempDBName(name string) bool {
	const prefix = "NetGoldImport_"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	if rest == "" {
		return false
	}
	for _, ch := range rest {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// sqlLiteral quotes a string as a T-SQL N'...' literal.
func sqlLiteral(s string) string {
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// bracket quotes an identifier as [ ... ] (only ever used with the
// validated temporary database name).
func bracket(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

type bakFile struct {
	logical string
	isLog   bool
}

// restoreFileList runs RESTORE FILELISTONLY and returns the backup's
// logical files.
func restoreFileList(db *sql.DB, bakPath string) ([]bakFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rows, err := db.QueryContext(ctx, "RESTORE FILELISTONLY FROM DISK = "+sqlLiteral(bakPath))
	if err != nil {
		return nil, fmt.Errorf("this does not look like a valid SQL Server backup file (%v)", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	colIdx := map[string]int{}
	for i, c := range cols {
		colIdx[strings.ToLower(c)] = i
	}
	li, lok := colIdx["logicalname"]
	ti, tok := colIdx["type"]
	if !lok || !tok {
		return nil, fmt.Errorf("unexpected RESTORE FILELISTONLY result — cannot read this backup")
	}
	var out []bakFile
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		typ := strOf(vals[ti])
		out = append(out, bakFile{logical: strOf(vals[li]), isLog: strings.EqualFold(typ, "L")})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the backup contains no database files — it may be a log-only backup, which cannot be imported")
	}
	return out, nil
}

// defaultDataDir asks the server where its databases live, so restored
// files can be placed there (a location the SQL Server service account
// is guaranteed to be able to write to).
func defaultDataDir(db *sql.DB) (dir string, sep string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var sample sql.NullString
	// Preferred: the instance default data path.
	err = db.QueryRowContext(ctx,
		`SELECT CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(4000))`).Scan(&sample)
	if err == nil && sample.Valid && strings.TrimSpace(sample.String) != "" {
		return splitDir(sample.String)
	}
	// Fallback: the folder of the master database's own data file.
	sample = sql.NullString{}
	err = db.QueryRowContext(ctx,
		`SELECT TOP 1 physical_name FROM sys.master_files WHERE database_id = DB_ID('master') AND type = 0`).Scan(&sample)
	if err != nil {
		return "", "", fmt.Errorf("cannot determine the SQL Server data folder: %v", err)
	}
	return splitDir(sample.String)
}

// splitDir splits a full file path into its folder and the path
// separator style the server uses.
func splitDir(fullPath string) (dir string, sep string, err error) {
	sep = "/"
	if strings.Contains(fullPath, `\`) {
		sep = `\`
	}
	idx := strings.LastIndex(fullPath, sep)
	if idx < 0 {
		return "", "", fmt.Errorf("unexpected data file path from SQL Server: %s", fullPath)
	}
	return fullPath[:idx], sep, nil
}

func joinServerPath(dir, sep, name string) string {
	return strings.TrimRight(dir, `/\`) + sep + name
}

// dropTempDB drops a temporary import database. It refuses any name
// that does not match the generated temporary pattern.
func dropTempDB(db *sql.DB, name string) {
	if !ValidTempDBName(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, "ALTER DATABASE "+bracket(name)+" SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
	_, _ = db.ExecContext(ctx, "DROP DATABASE "+bracket(name))
}

// ImportBak restores bakPath into a temporary database on the server
// described by cfg (a working connection, e.g. from auto-detect), runs
// the normal the legacy system import from the restored database into st,
// and drops the temporary database in all outcomes.
func ImportBak(cfg ConnConfig, bakPath string, st *store.Store) (*Report, error) {
	return ImportBakWithProgress(cfg, bakPath, st, nil)
}

// ImportBakWithProgress is ImportBak plus an optional phase callback
// ("restoring" just before the RESTORE runs, "importing" just before
// the import from the restored database starts). The automatic engine
// setup uses it to drive its step display; other callers pass nil.
func ImportBakWithProgress(cfg ConnConfig, bakPath string, st *store.Store, onPhase func(phase string)) (*Report, error) {
	phase := func(p string) {
		if onPhase != nil {
			onPhase(p)
		}
	}
	if err := ValidateBakPath(bakPath); err != nil {
		return nil, err
	}
	masterCfg := cfg
	masterCfg.Database = "master"
	master, err := tryConnect(masterCfg, 10*time.Second)
	if err != nil {
		// Name the target and the sign-in mode, not just the raw
		// driver error (v3.0.1 — see FormatConnectError).
		return nil, fmt.Errorf("%s", FormatConnectError(masterCfg, err, CurrentSessionContext()))
	}
	defer master.Close()

	files, err := restoreFileList(master.DB, bakPath)
	if err != nil {
		return nil, fmt.Errorf("reading the backup file: %v", err)
	}
	dir, sep, err := defaultDataDir(master.DB)
	if err != nil {
		return nil, fmt.Errorf("preparing the restore: %v", err)
	}

	tempName := TempDBName(time.Now())
	if !ValidTempDBName(tempName) { // internal invariant, cannot happen
		return nil, fmt.Errorf("internal error: generated an invalid temporary database name")
	}

	var moves []string
	dataN, logN := 0, 0
	for _, f := range files {
		var phys string
		if f.isLog {
			logN++
			if logN == 1 {
				phys = joinServerPath(dir, sep, tempName+"_log.ldf")
			} else {
				phys = joinServerPath(dir, sep, fmt.Sprintf("%s_log%d.ldf", tempName, logN))
			}
		} else {
			dataN++
			if dataN == 1 {
				phys = joinServerPath(dir, sep, tempName+".mdf")
			} else {
				phys = joinServerPath(dir, sep, fmt.Sprintf("%s_%d.ndf", tempName, dataN))
			}
		}
		moves = append(moves, "MOVE "+sqlLiteral(f.logical)+" TO "+sqlLiteral(phys))
	}
	restoreSQL := "RESTORE DATABASE " + bracket(tempName) +
		" FROM DISK = " + sqlLiteral(bakPath) +
		" WITH " + strings.Join(moves, ", ") + ", RECOVERY"
	phase("restoring")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	_, restoreErr := master.DB.ExecContext(ctx, restoreSQL)
	cancel()
	if restoreErr != nil {
		return nil, fmt.Errorf("restoring the backup: %v (Tip: the SQL Server service account needs permission to read the backup file and to write to its data folder.)", restoreErr)
	}
	// From here on the temporary database exists: always drop it.
	defer dropTempDB(master.DB, tempName)

	tempCfg := cfg
	tempCfg.Database = tempName
	src, err := tryConnect(tempCfg, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("opening the restored database: %s", FormatConnectError(tempCfg, err, CurrentSessionContext()))
	}
	defer src.Close()
	phase("importing")
	rep, err := RunImport(src, st)
	if err != nil {
		return nil, fmt.Errorf("importing from the restored database: %v", err)
	}
	return rep, nil
}
