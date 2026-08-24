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
	DB *sql.DB
}

type Mapping struct {
	LocalID      string
	Kind         string
	ObjectID     string
	CollectionID string
	ContentHash  string
	Revision     uint64
	StartMS      int64
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
	return &Map{DB: db}, nil
}

func (m *Map) Close() error { return m.DB.Close() }

func (m *Map) Cursor() int64 {
	var v sql.NullInt64
	_ = m.DB.QueryRow(`SELECT v FROM meta WHERE k = 'cursor'`).Scan(&v)
	if v.Valid {
		return v.Int64
	}
	return 0
}

func (m *Map) SetCursor(seq int64) error {
	_, err := m.DB.Exec(`INSERT INTO meta(k, v) VALUES ('cursor', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, seq)
	return err
}

func (m *Map) Put(row Mapping) error {
	_, err := m.DB.Exec(`
		INSERT INTO idmap(local_id, kind, object_id, collection_id, content_hash, revision, start_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(local_id, kind) DO UPDATE SET
			object_id = excluded.object_id,
			collection_id = excluded.collection_id,
			content_hash = excluded.content_hash,
			revision = excluded.revision,
			start_ms = excluded.start_ms
	`, row.LocalID, row.Kind, row.ObjectID, row.CollectionID, row.ContentHash, row.Revision, row.StartMS)
	return err
}

func (m *Map) ByLocal(kind, localID string) (Mapping, error) {
	var r Mapping
	err := m.DB.QueryRow(`
		SELECT local_id, kind, object_id, collection_id, content_hash, revision, start_ms
		FROM idmap WHERE kind = ? AND local_id = ?
	`, kind, localID).Scan(&r.LocalID, &r.Kind, &r.ObjectID, &r.CollectionID, &r.ContentHash, &r.Revision, &r.StartMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Mapping{}, sql.ErrNoRows
	}
	return r, err
}

func (m *Map) ByObject(objectID string) (Mapping, error) {
	var r Mapping
	err := m.DB.QueryRow(`
		SELECT local_id, kind, object_id, collection_id, content_hash, revision, start_ms
		FROM idmap WHERE object_id = ?
	`, objectID).Scan(&r.LocalID, &r.Kind, &r.ObjectID, &r.CollectionID, &r.ContentHash, &r.Revision, &r.StartMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Mapping{}, sql.ErrNoRows
	}
	return r, err
}

func (m *Map) ForCollection(collectionID string) ([]Mapping, error) {
	rows, err := m.DB.Query(`
		SELECT local_id, kind, object_id, collection_id, content_hash, revision, start_ms
		FROM idmap WHERE collection_id = ?
	`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mapping
	for rows.Next() {
		var r Mapping
		if err := rows.Scan(&r.LocalID, &r.Kind, &r.ObjectID, &r.CollectionID, &r.ContentHash, &r.Revision, &r.StartMS); err != nil {
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
