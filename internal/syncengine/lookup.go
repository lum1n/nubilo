package syncengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	rows, err := e.Store.DB.QueryContext(ctx, `SELECT `+collectionCols+collectionFrom+` WHERE c.id = ?`, id)
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

// CountLiveObjects returns live objects in one collection.
func (e *Engine) CountLiveObjects(ctx context.Context, collectionID string) (int, error) {
	var n int
	err := e.Store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM objects
		WHERE collection_id = ? AND deleted_at IS NULL
	`, collectionID).Scan(&n)
	return n, err
}

// ChildCollections lists live children of parentID, or, with parentID empty,
// the live roots owned by ownerUserID ("" = roots of every owner; system use).
func (e *Engine) ChildCollections(ctx context.Context, ownerUserID, kind, parentID string) ([]Collection, error) {
	var rows *sql.Rows
	var err error
	switch {
	case parentID != "":
		rows, err = e.Store.DB.QueryContext(ctx, `SELECT `+collectionCols+collectionFrom+`
			WHERE c.kind = ? AND c.deleted_at IS NULL AND c.parent_id = ?
			ORDER BY c.name`, kind, parentID)
	case ownerUserID != "":
		rows, err = e.Store.DB.QueryContext(ctx, `SELECT `+collectionCols+collectionFrom+`
			WHERE c.kind = ? AND c.deleted_at IS NULL AND c.parent_id IS NULL AND c.owner_user_id = ?
			ORDER BY c.name`, kind, ownerUserID)
	default:
		rows, err = e.Store.DB.QueryContext(ctx, `SELECT `+collectionCols+collectionFrom+`
			WHERE c.kind = ? AND c.deleted_at IS NULL AND c.parent_id IS NULL
			ORDER BY c.name`, kind)
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

// SharedRoots lists live root collections of kind that other users shared
// with userID, with Access set.
func (e *Engine) SharedRoots(ctx context.Context, userID, kind string) ([]Collection, error) {
	if userID == "" {
		return nil, nil
	}
	// A collection shared both directly and through groups appears once,
	// with the best access.
	rows, err := e.Store.DB.QueryContext(ctx, `SELECT `+collectionCols+`, MAX(`+shareAccessSQL+`)`+collectionFrom+`
		JOIN shares s ON s.collection_id = c.id AND `+shareMatchSQL+`
		WHERE c.kind = ? AND c.deleted_at IS NULL AND c.parent_id IS NULL AND COALESCE(c.owner_user_id, '') <> ?
		  AND COALESCE(u.status, 'active') = 'active'
		GROUP BY c.id
		ORDER BY u.username, c.name`, userID, userID, kind, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCollectionsWithAccess(rows)
}

func (e *Engine) FindChildCollection(ctx context.Context, ownerUserID, kind, parentID, name string) (*Collection, error) {
	children, err := e.ChildCollections(ctx, ownerUserID, kind, parentID)
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

// CollectionCTags returns an opaque change tag per live collection of kind.
// Tags bump when objects change (journal) or when collection metadata is updated.
func (e *Engine) CollectionCTags(ctx context.Context, sc Scope, kind string) (map[string]string, error) {
	where, args := sc.filter()
	rows, err := e.Store.DB.QueryContext(ctx, `
		SELECT c.id, c.updated_at, COALESCE(MAX(j.seq), 0)
		FROM collections c
		LEFT JOIN journal j ON j.collection_id = c.id
		WHERE c.kind = ? AND c.deleted_at IS NULL AND `+where+`
		GROUP BY c.id
	`, append([]any{kind}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id string
		var updatedAt, maxSeq int64
		if err := rows.Scan(&id, &updatedAt, &maxSeq); err != nil {
			return nil, err
		}
		out[id] = fmt.Sprintf("%d-%d", maxSeq, updatedAt)
	}
	return out, rows.Err()
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
		return mapCollectionErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCollectionParent moves a collection (and its subtree) under parentID, or
// makes it a root. Moves across owners are rejected.
func (e *Engine) SetCollectionParent(ctx context.Context, id, parentID string) error {
	c, err := e.GetCollection(ctx, id)
	if err != nil {
		return err
	}
	if c.DeletedAt != nil {
		return ErrNotFound
	}
	newRoot := id
	var parent any
	if parentID != "" {
		p, err := e.GetCollection(ctx, parentID)
		if err != nil {
			return err
		}
		if p.OwnerUserID != c.OwnerUserID {
			return ErrCollection
		}
		parent = parentID
		newRoot = p.RootID
	}
	now := store.NowMS()
	return e.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE collections SET parent_id = ?, updated_at = ?, revision = revision + 1
			WHERE id = ? AND deleted_at IS NULL
		`, parent, now, id)
		if err != nil {
			return mapCollectionErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(`
			WITH RECURSIVE sub(id) AS (
				SELECT ? UNION ALL SELECT c.id FROM collections c JOIN sub ON c.parent_id = sub.id
			)
			UPDATE collections SET root_id = ? WHERE id IN (SELECT id FROM sub)
		`, id, newRoot)
		return err
	})
}

func (e *Engine) TombstoneCollection(ctx context.Context, id string) error {
	now := store.NowMS()
	return e.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE collections SET deleted_at = ?, updated_at = ?, revision = revision + 1
			WHERE id = ? AND deleted_at IS NULL
		`, now, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM shares WHERE collection_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM collection_prefs WHERE collection_id = ?`, id)
		return err
	})
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

// ListObjectsPage lists live objects of kind ("" = any) in a collection in id
// order, starting after afterID.
func (e *Engine) ListObjectsPage(ctx context.Context, collectionID, kind, afterID string, limit int) ([]Object, error) {
	rows, err := e.Store.DB.QueryContext(ctx, objectSelect+`
		WHERE collection_id = ? AND deleted_at IS NULL AND (? = '' OR kind = ?) AND id > ?
		ORDER BY id LIMIT ?`, collectionID, kind, kind, afterID, limit)
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

// FindLiveObjectByBlob returns a live object in a collection whose blob is hash.
func (e *Engine) FindLiveObjectByBlob(ctx context.Context, collectionID, hash string) (*Object, error) {
	row := e.Store.DB.QueryRowContext(ctx, objectSelect+`
		WHERE collection_id = ? AND deleted_at IS NULL AND blob_id = ? LIMIT 1`, collectionID, hash)
	return scanObject(row)
}

// Usage is the logical storage of one user (deduplicated blobs count once
// per object that uses them).
type Usage struct {
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// UserUsage returns live object counts and bytes per owning user. Bytes are
// what counts toward quotas (objects.charged: data, metadata and photo
// renditions).
func (e *Engine) UserUsage(ctx context.Context) (map[string]Usage, error) {
	rows, err := e.Store.DB.QueryContext(ctx, `
		SELECT COALESCE(c.owner_user_id, ''), COUNT(o.id), CAST(MIN(TOTAL(o.charged), 9.2e18) AS INTEGER)
		FROM objects o JOIN collections c ON c.id = o.collection_id
		WHERE o.deleted_at IS NULL AND c.deleted_at IS NULL
		GROUP BY c.owner_user_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Usage{}
	for rows.Next() {
		var id string
		var u Usage
		if err := rows.Scan(&id, &u.Objects, &u.Bytes); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

// Username returns the username of userID.
func (e *Engine) Username(ctx context.Context, userID string) (string, error) {
	var name string
	err := e.Store.DB.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
