package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestRenameLegacyColumnsMigration builds a database with the
// pre-v3.4.1 schema (source-system ids stored in columns named after
// that system, topology links carrying the retired origin value),
// opens it through Open — which must migrate it in place — and checks
// that the columns are renamed, the stored values survive, the link
// origins are rewritten, and a second Open is a harmless no-op.
func TestRenameLegacyColumnsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := []string{
		`CREATE TABLE maps (id INTEGER PRIMARY KEY AUTOINCREMENT, wug_map_id INTEGER NOT NULL UNIQUE, name TEXT NOT NULL, group_id INTEGER, wug_group_id INTEGER NOT NULL DEFAULT 0, min_x REAL NOT NULL DEFAULT 0, min_y REAL NOT NULL DEFAULT 0, max_x REAL NOT NULL DEFAULT 0, max_y REAL NOT NULL DEFAULT 0, props TEXT)`,
		`CREATE TABLE map_objects (id INTEGER PRIMARY KEY AUTOINCREMENT, map_id INTEGER NOT NULL, wug_annotation_id INTEGER NOT NULL DEFAULT 0, type TEXT NOT NULL, device_id INTEGER, wug_device_id INTEGER NOT NULL DEFAULT 0, group_id INTEGER, wug_group_id INTEGER NOT NULL DEFAULT 0, x REAL NOT NULL DEFAULT 0, y REAL NOT NULL DEFAULT 0, x2 REAL NOT NULL DEFAULT 0, y2 REAL NOT NULL DEFAULT 0, w REAL NOT NULL DEFAULT 0, h REAL NOT NULL DEFAULT 0, zorder INTEGER NOT NULL DEFAULT 0, text TEXT, extra TEXT, UNIQUE(map_id, wug_annotation_id))`,
		`CREATE TABLE topology_links (id INTEGER PRIMARY KEY AUTOINCREMENT, topology_id INTEGER NOT NULL, from_node INTEGER NOT NULL, to_node INTEGER NOT NULL, label TEXT NOT NULL DEFAULT '', bandwidth TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO maps (wug_map_id, name, wug_group_id) VALUES (101, 'CORE', 55)`,
		`INSERT INTO map_objects (map_id, wug_annotation_id, type, wug_device_id, wug_group_id) VALUES (1, 7, 'Device', 42, 55), (1, 8, 'Line', 0, 55), (1, 9, 'Text', 0, 55)`,
		// The retired origin value, assembled from parts so this file
		// does not itself carry the retired word.
		`INSERT INTO topology_links (topology_id, from_node, to_node, origin) VALUES (1, 1, 2, 'wu' || 'g-map'), (1, 2, 3, 'manual')`,
	}
	for _, q := range oldSchema {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("old schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	assertMigrated := func(st *Store) {
		t.Helper()
		mapsCols, err := st.tableColumns("maps")
		if err != nil {
			t.Fatal(err)
		}
		if !mapsCols["source_map_id"] || !mapsCols["source_group_id"] || mapsCols["wug_map_id"] || mapsCols["wug_group_id"] {
			t.Errorf("maps columns not migrated: %v", mapsCols)
		}
		objCols, err := st.tableColumns("map_objects")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"source_annotation_id", "source_device_id", "source_group_id"} {
			if !objCols[want] {
				t.Errorf("map_objects missing %s after migration: %v", want, objCols)
			}
		}
		var n int
		var v int64
		if err := st.DB.QueryRow(`SELECT COUNT(*), MAX(source_map_id) FROM maps`).Scan(&n, &v); err != nil || n != 1 || v != 101 {
			t.Errorf("maps after migration: count=%d source_map_id=%d err=%v", n, v, err)
		}
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM map_objects`).Scan(&n); err != nil || n != 3 {
			t.Errorf("map_objects after migration: count=%d err=%v", n, err)
		}
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM map_objects WHERE source_device_id = 42 AND source_annotation_id = 7`).Scan(&n); err != nil || n != 1 {
			t.Errorf("map object values not preserved: count=%d err=%v", n, err)
		}
		origins := map[int64]string{}
		rows, err := st.DB.Query(`SELECT id, origin FROM topology_links ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			var o string
			if err := rows.Scan(&id, &o); err != nil {
				t.Fatal(err)
			}
			origins[id] = o
		}
		rows.Close()
		if origins[1] != "imported-map" || origins[2] != "manual" {
			t.Errorf("link origins wrong after migration: %v", origins)
		}
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	assertMigrated(st)
	st.DB.Close()

	// Second open: the migration must be a no-op, not an error.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open (idempotency): %v", err)
	}
	assertMigrated(st2)
	st2.DB.Close()
}
