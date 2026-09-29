package agent

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"nubilo/internal/syncengine"

	_ "modernc.org/sqlite"
)

type Map struct {
	DB   *sql.DB
	Path string // sqlite file path; used for contact-cache sidecars
}

type Mapping struct {
	LocalID      string
	Kind         string
	ObjectID     string
	CollectionID string
	ContentHash  string
	Revision     uint64
	StartMS      int64
	ModMS        int64
	UID          string
	Name         string
	LocalHash    string
}

func OpenMap(path string) (*Map, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS idmap (
			local_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			object_id TEXT NOT NULL,
			collection_id TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			revision INTEGER NOT NULL,
			start_ms INTEGER NOT NULL DEFAULT 0,
			mod_ms INTEGER NOT NULL DEFAULT 0,
			uid TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			local_hash TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (local_id, kind)
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idmap_object ON idmap(object_id);
		CREATE TABLE IF NOT EXISTS meta (
			k TEXT PRIMARY KEY,
			v INTEGER NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateIDMap(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Map{DB: db, Path: path}, nil
}

func migrateIDMap(db *sql.DB) error {
	if err := ensureIDMapColumn(db, "mod_ms", `ALTER TABLE idmap ADD COLUMN mod_ms INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureIDMapColumn(db, "uid", `ALTER TABLE idmap ADD COLUMN uid TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureIDMapColumn(db, "name", `ALTER TABLE idmap ADD COLUMN name TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	added, err := ensureIDMapColumnAdded(db, "local_hash", `ALTER TABLE idmap ADD COLUMN local_hash TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		return err
	}
	if added {
		// Existing rows were pushed by the agent, so local render matched content_hash.
		if _, err := db.Exec(`UPDATE idmap SET local_hash = content_hash WHERE local_hash = ''`); err != nil {
			return err
		}
	}
	return nil
}

func ensureIDMapColumn(db *sql.DB, name, alterSQL string) error {
	_, err := ensureIDMapColumnAdded(db, name, alterSQL)
	return err
}

func ensureIDMapColumnAdded(db *sql.DB, name, alterSQL string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('idmap') WHERE name = ?`, name).Scan(&n)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := db.Exec(alterSQL); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Map) Close() error { return m.DB.Close() }

func (m *Map) Cursor() int64 {
	return m.MetaInt("cursor")
}

func (m *Map) SetCursor(seq int64) error {
	return m.SetMetaInt("cursor", seq)
}

func (m *Map) MetaInt(k string) int64 {
	var v sql.NullInt64
	_ = m.DB.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	if v.Valid {
		return v.Int64
	}
	return 0
}

func (m *Map) SetMetaInt(k string, v int64) error {
	_, err := m.DB.Exec(`INSERT INTO meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

func (m *Map) Put(row Mapping) error {
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// EventKit can return a new calendarItemIdentifier for the same server
	// object (recurring save, pull-after-push). Rebind instead of inserting
	// a second row, which trips UNIQUE(object_id).
	if row.ObjectID != "" {
		if _, err := tx.Exec(`DELETE FROM idmap WHERE object_id = ? AND (local_id != ? OR kind != ?)`,
			row.ObjectID, row.LocalID, row.Kind); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`
		INSERT INTO idmap(local_id, kind, object_id, collection_id, content_hash, revision, start_ms, mod_ms, uid, name, local_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(local_id, kind) DO UPDATE SET
			object_id = excluded.object_id,
			collection_id = excluded.collection_id,
			content_hash = excluded.content_hash,
			revision = excluded.revision,
			start_ms = excluded.start_ms,
			mod_ms = excluded.mod_ms,
			uid = excluded.uid,
			name = excluded.name,
			local_hash = excluded.local_hash
	`, row.LocalID, row.Kind, row.ObjectID, row.CollectionID, row.ContentHash, row.Revision, row.StartMS, row.ModMS, row.UID, row.Name, row.LocalHash)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func scanMapping(sc interface{ Scan(dest ...any) error }) (Mapping, error) {
	var r Mapping
	err := sc.Scan(&r.LocalID, &r.Kind, &r.ObjectID, &r.CollectionID, &r.ContentHash, &r.Revision, &r.StartMS, &r.ModMS, &r.UID, &r.Name, &r.LocalHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Mapping{}, sql.ErrNoRows
	}
	return r, err
}

const mappingSelect = `SELECT local_id, kind, object_id, collection_id, content_hash, revision, start_ms, mod_ms, uid, name, local_hash FROM idmap`

func (m *Map) ByLocal(kind, localID string) (Mapping, error) {
	return scanMapping(m.DB.QueryRow(mappingSelect+` WHERE kind = ? AND local_id = ?`, kind, localID))
}

func (m *Map) ByObject(objectID string) (Mapping, error) {
	return scanMapping(m.DB.QueryRow(mappingSelect+` WHERE object_id = ?`, objectID))
}

func (m *Map) ForCollection(collectionID string) ([]Mapping, error) {
	rows, err := m.DB.Query(mappingSelect+` WHERE collection_id = ?`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mapping
	for rows.Next() {
		r, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *Map) DeleteObject(objectID string) error {
	_, err := m.DB.Exec(`DELETE FROM idmap WHERE object_id = ?`, objectID)
	return err
}

func (m *Map) Inventory(collectionID string) ([]syncengine.InventoryItem, error) {
	rows, err := m.ForCollection(collectionID)
	if err != nil {
		return nil, err
	}
	out := make([]syncengine.InventoryItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, syncengine.InventoryItem{ID: r.ObjectID, Revision: r.Revision, ContentHash: r.ContentHash})
	}
	return out, nil
}
