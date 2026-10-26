package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"nubilo/internal/ids"
)

// OwnerUsername is the admin user that inherits all data when a single-user
// (schema v1) database is upgraded, and the default user for CLI pairing.
const OwnerUsername = "owner"

type migration struct {
	Version int
	Name    string
	Up      func(ctx context.Context, tx *sql.Tx) error
}

var migrations = []migration{
	{Version: 2, Name: "multi-user", Up: migrateV2},
}

// SchemaVersion returns the highest applied schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_meta`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// Migrate applies pending schema migrations, each in its own write transaction.
func (s *Store) Migrate(ctx context.Context) error {
	cur, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.Version <= cur {
			continue
		}
		err := s.WithWrite(ctx, func(tx *sql.Tx) error {
			if err := m.Up(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_meta(version) VALUES (?)`, m.Version)
			return err
		})
		if err != nil {
			return fmt.Errorf("v%d %s: %w", m.Version, m.Name, err)
		}
		cur = m.Version
	}
	return nil
}

func columnExists(ctx context.Context, tx *sql.Tx, table, col string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n)
	return n > 0, err
}

func addColumn(ctx context.Context, tx *sql.Tx, table, col, def string) error {
	ok, err := columnExists(ctx, tx, table, col)
	if err != nil || ok {
		return err
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, col, def))
	return err
}

func migrateV2(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			role TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL
		);
	`); err != nil {
		return err
	}
	cols := []struct{ table, col, def string }{
		{"devices", "user_id", "TEXT REFERENCES users(id)"},
		{"app_passwords", "user_id", "TEXT REFERENCES users(id)"},
		{"app_passwords", "label", "TEXT NOT NULL DEFAULT ''"},
		{"pairing_sessions", "user_id", "TEXT"},
		{"collections", "owner_user_id", "TEXT REFERENCES users(id)"},
		{"collections", "root_id", "TEXT"},
		{"audit", "user_id", "TEXT"},
	}
	for _, c := range cols {
		if err := addColumn(ctx, tx, c.table, c.col, c.def); err != nil {
			return fmt.Errorf("%s.%s: %w", c.table, c.col, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS shares (
			collection_id TEXT NOT NULL,
			principal_type TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			access TEXT NOT NULL,
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (collection_id, principal_type, principal_id)
		);
		CREATE TABLE IF NOT EXISTS invites (
			id TEXT PRIMARY KEY,
			code_hash BLOB NOT NULL,
			code_salt BLOB NOT NULL,
			role TEXT NOT NULL,
			user_id TEXT,
			max_uses INTEGER NOT NULL DEFAULT 1,
			uses INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL,
			created_by TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS blob_refs (
			user_id TEXT NOT NULL,
			hash TEXT NOT NULL,
			PRIMARY KEY (user_id, hash)
		);
		CREATE TABLE IF NOT EXISTS collection_prefs (
			user_id TEXT NOT NULL,
			collection_id TEXT NOT NULL,
			color TEXT,
			sort_order INTEGER,
			PRIMARY KEY (user_id, collection_id)
		);
		CREATE TABLE IF NOT EXISTS profile_tokens (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			payload BLOB NOT NULL,
			expires_at INTEGER NOT NULL,
			used_at INTEGER
		);
		CREATE TABLE IF NOT EXISTS upload_tokens (
			id TEXT PRIMARY KEY,
			device_id TEXT NOT NULL,
			hash BLOB NOT NULL,
			expires_at INTEGER NOT NULL
		);
		DROP TABLE IF EXISTS blob_uploads;
		CREATE INDEX IF NOT EXISTS blob_refs_hash ON blob_refs(hash);
		CREATE INDEX IF NOT EXISTS shares_principal ON shares(principal_type, principal_id);
		CREATE INDEX IF NOT EXISTS collections_root ON collections(root_id);
		CREATE INDEX IF NOT EXISTS devices_user ON devices(user_id);
		CREATE INDEX IF NOT EXISTS objects_blob ON objects(blob_id);
	`); err != nil {
		return err
	}

	// Push idempotency keys are now scoped per device.
	if _, err := tx.ExecContext(ctx, `
		UPDATE push_idempotency SET key = device_id || ':' || key
		WHERE substr(key, 1, length(device_id) + 1) <> device_id || ':'
	`); err != nil {
		return err
	}

	// Every collection gets a root: itself for roots, the topmost ancestor otherwise.
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE tree(id, root) AS (
			SELECT id, id FROM collections WHERE parent_id IS NULL OR parent_id = ''
			UNION ALL
			SELECT c.id, t.root FROM collections c JOIN tree t ON c.parent_id = t.id
		)
		UPDATE collections SET root_id = (SELECT root FROM tree WHERE tree.id = collections.id)
		WHERE root_id IS NULL
	`); err != nil {
		return err
	}
	// Orphans (parent missing) become their own roots.
	if _, err := tx.ExecContext(ctx, `UPDATE collections SET root_id = id WHERE root_id IS NULL`); err != nil {
		return err
	}

	var devices, collections int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&devices); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections`).Scan(&collections); err != nil {
		return err
	}
	if devices > 0 || collections > 0 {
		if err := adoptSingleUserData(ctx, tx); err != nil {
			return err
		}
	}

	if err := dedupeCollectionNames(ctx, tx); err != nil {
		return err
	}
	// live_pair_hash became a counted metadata blob; recount from live objects.
	if err := recountBlobRefs(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS collections_owner_name
		ON collections(owner_user_id, kind, COALESCE(parent_id, ''), name) WHERE deleted_at IS NULL
	`)
	return err
}

// adoptSingleUserData assigns everything in a v1 database to a new admin user.
func adoptSingleUserData(ctx context.Context, tx *sql.Tx) error {
	ownerID := ids.New()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users(id, username, display_name, role, status, created_at)
		VALUES (?, ?, 'Owner', 'admin', 'active', ?)
	`, ownerID, OwnerUsername, NowMS()); err != nil {
		return err
	}
	stmts := []string{
		`UPDATE devices SET user_id = ? WHERE user_id IS NULL`,
		`UPDATE app_passwords SET user_id = ? WHERE user_id IS NULL`,
		`UPDATE collections SET owner_user_id = ? WHERE owner_user_id IS NULL`,
		`INSERT OR IGNORE INTO blob_refs(user_id, hash) SELECT ?, id FROM blobs`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, ownerID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO collection_prefs(user_id, collection_id, color, sort_order)
		SELECT ?, id, json_extract(metadata, '$.color'), json_extract(metadata, '$.order')
		FROM collections
		WHERE kind = 'calendar' AND json_valid(metadata)
		  AND (json_extract(metadata, '$.color') IS NOT NULL OR json_extract(metadata, '$.order') IS NOT NULL)
	`, ownerID)
	return err
}

// dedupeCollectionNames renames live siblings that share a name so the
// per-owner unique index can be created. The oldest keeps its name.
func dedupeCollectionNames(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name FROM (
			SELECT id, name, ROW_NUMBER() OVER (
				PARTITION BY COALESCE(owner_user_id, ''), kind, COALESCE(parent_id, ''), name
				ORDER BY created_at, id
			) AS rn
			FROM collections WHERE deleted_at IS NULL
		) WHERE rn > 1
	`)
	if err != nil {
		return err
	}
	type dup struct{ id, name string }
	var dups []dup
	for rows.Next() {
		var d dup
		if err := rows.Scan(&d.id, &d.name); err != nil {
			rows.Close()
			return err
		}
		dups = append(dups, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range dups {
		suffix := d.id
		if len(suffix) > 6 {
			suffix = suffix[len(suffix)-6:]
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET name = ? WHERE id = ?`, d.name+" ("+suffix+")", d.id); err != nil {
			return err
		}
	}
	return nil
}

func recountBlobRefs(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT blob_id, metadata FROM objects WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	refs := map[string]int{}
	for rows.Next() {
		var blob sql.NullString
		var meta string
		if err := rows.Scan(&blob, &meta); err != nil {
			rows.Close()
			return err
		}
		if blob.Valid && blob.String != "" {
			refs[blob.String]++
		}
		for _, h := range MetadataBlobIDs(json.RawMessage(meta)) {
			refs[h]++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = 0`); err != nil {
		return err
	}
	for h, n := range refs {
		if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = ? WHERE id = ?`, n, h); err != nil {
			return err
		}
	}
	return nil
}
