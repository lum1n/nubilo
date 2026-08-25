package syncengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/store"
)

const objectSelect = `
	SELECT id, collection_id, kind, revision, content_hash, metadata_hash, blob_id, size, origin_device, metadata, created_at, updated_at, deleted_at
	FROM objects
`

func (e *Engine) GetObject(ctx context.Context, id string) (*Object, error) {
	row := e.Store.DB.QueryRowContext(ctx, objectSelect+` WHERE id = ?`, id)
	return scanObject(row)
}

func (e *Engine) GetCollection(ctx context.Context, id string) (*Collection, error) {
	rows, err := e.Store.DB.QueryContext(ctx, `
		SELECT id, kind, name, parent_id, revision, metadata, metadata_hash, created_at, updated_at, deleted_at
		FROM collections WHERE id = ?
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	c, err := scanCollection(rows)
	if err != nil {
		return nil, err
	}
	return &c, rows.Err()
}

func (e *Engine) ListObjects(ctx context.Context, collectionID string) ([]Object, error) {
	rows, err := e.Store.DB.QueryContext(ctx, objectSelect+` WHERE collection_id = ? AND deleted_at IS NULL ORDER BY created_at`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// CountLiveObjectsByKind returns live objects in all live collections of kind
// (including nested file folders).
func (e *Engine) CountLiveObjectsByKind(ctx context.Context, kind string) (int, error) {
	var n int
	err := e.Store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM objects o
		JOIN collections c ON c.id = o.collection_id
		WHERE c.kind = ? AND c.deleted_at IS NULL AND o.deleted_at IS NULL
	`, kind).Scan(&n)
	return n, err
}

func (e *Engine) ChildCollections(ctx context.Context, kind, parentID string) ([]Collection, error) {
	var rows *sql.Rows
	var err error
	if parentID == "" {
		rows, err = e.Store.DB.QueryContext(ctx, `
			SELECT id, kind, name, parent_id, revision, metadata, metadata_hash, created_at, updated_at, deleted_at
			FROM collections
			WHERE kind = ? AND deleted_at IS NULL AND parent_id IS NULL
			ORDER BY name
		`, kind)
	} else {
		rows, err = e.Store.DB.QueryContext(ctx, `
			SELECT id, kind, name, parent_id, revision, metadata, metadata_hash, created_at, updated_at, deleted_at
			FROM collections
			WHERE kind = ? AND deleted_at IS NULL AND parent_id = ?
			ORDER BY name
		`, kind, parentID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (e *Engine) FindChildCollection(ctx context.Context, kind, parentID, name string) (*Collection, error) {
	children, err := e.ChildCollections(ctx, kind, parentID)
	if err != nil {
		return nil, err
	}
	for i := range children {
		if children[i].Name == name {
			return &children[i], nil
		}
	}
	return nil, ErrNotFound
}

func (e *Engine) FindObjectByName(ctx context.Context, collectionID, name string) (*Object, error) {
	row := e.Store.DB.QueryRowContext(ctx, objectSelect+`
		WHERE collection_id = ? AND deleted_at IS NULL AND json_extract(metadata, '$.name') = ?
	`, collectionID, name)
	return scanObject(row)
}

func (e *Engine) FindObjectByUID(ctx context.Context, collectionID, uid string) (*Object, error) {
	if uid == "" {
		return nil, ErrNotFound
	}
	row := e.Store.DB.QueryRowContext(ctx, objectSelect+`
		WHERE collection_id = ? AND deleted_at IS NULL AND json_extract(metadata, '$.uid') = ?
	`, collectionID, uid)
	return scanObject(row)
}

func (e *Engine) SetCollectionMetadata(ctx context.Context, id string, metadata json.RawMessage) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if !json.Valid(metadata) {
		return ErrCollection
	}
	c, err := e.GetCollection(ctx, id)
	if err != nil {
		return err
	}
	if c.DeletedAt != nil {
		return ErrNotFound
	}
	hash := ncrypto.SHA256Hex(metadata)
	if c.MetadataHash == hash {
		return nil
	}
	now := store.NowMS()
	res, err := e.Store.DB.ExecContext(ctx, `
		UPDATE collections SET metadata = ?, metadata_hash = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND deleted_at IS NULL
	`, string(metadata), hash, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (e *Engine) PatchCollectionMetadata(ctx context.Context, id string, patch json.RawMessage) (*Collection, error) {
	c, err := e.GetCollection(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.DeletedAt != nil {
		return nil, ErrNotFound
	}
	merged, err := MergeJSON(c.Metadata, patch)
	if err != nil {
		return nil, ErrCollection
	}
	if err := e.SetCollectionMetadata(ctx, id, merged); err != nil {
		return nil, err
	}
	return e.GetCollection(ctx, id)
}

// MergeJSON shallow-merges patch object keys into base. Non-objects fail.
func MergeJSON(base, patch json.RawMessage) (json.RawMessage, error) {
	dst := map[string]any{}
	if len(base) > 0 && string(base) != "null" {
		if err := json.Unmarshal(base, &dst); err != nil {
			return nil, err
		}
	}
	if len(patch) == 0 || string(patch) == "null" {
		b, err := json.Marshal(dst)
		return b, err
	}
	var src map[string]any
	if err := json.Unmarshal(patch, &src); err != nil {
		return nil, err
	}
	for k, v := range src {
		dst[k] = v
	}
	return json.Marshal(dst)
}

func (e *Engine) RenameCollection(ctx context.Context, id, name string) error {
	if name == "" {
		return ErrCollection
	}
	now := store.NowMS()
	res, err := e.Store.DB.ExecContext(ctx, `
		UPDATE collections SET name = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND deleted_at IS NULL
	`, name, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (e *Engine) SetCollectionParent(ctx context.Context, id, parentID string) error {
	now := store.NowMS()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	res, err := e.Store.DB.ExecContext(ctx, `
		UPDATE collections SET parent_id = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND deleted_at IS NULL
	`, parent, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (e *Engine) TombstoneCollection(ctx context.Context, id string) error {
	now := store.NowMS()
	res, err := e.Store.DB.ExecContext(ctx, `
		UPDATE collections SET deleted_at = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanObject(sc scanner) (*Object, error) {
	var o Object
	var blob sql.NullString
	var origin sql.NullString
	var deleted sql.NullInt64
	var meta string
	err := sc.Scan(&o.ID, &o.CollectionID, &o.Kind, &o.Revision, &o.ContentHash, &o.MetadataHash, &blob, &o.Size, &origin, &meta, &o.CreatedAt, &o.UpdatedAt, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.BlobID = store.NullString(blob)
	o.OriginDevice = store.NullString(origin)
	o.Metadata = json.RawMessage(meta)
	o.DeletedAt = store.NullInt64(deleted)
	return &o, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func ModTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
