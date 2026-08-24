package syncengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
)

type Engine struct {
	Store *store.Store
}

func New(st *store.Store) *Engine {
	return &Engine{Store: st}
}

func (e *Engine) HeadSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := e.Store.DB.QueryRowContext(ctx, `SELECT MAX(seq) FROM journal`).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

func (e *Engine) Hello(ctx context.Context, cursor int64, restoreHint bool) (HelloResult, error) {
	head, err := e.HeadSeq(ctx)
	if err != nil {
		return HelloResult{}, err
	}
	h := HelloResult{Protocol: 1, ServerTimeMS: store.NowMS(), HeadSeq: head}
	if cursor < 0 {
		h.NeedReconcile = true
		h.Reason = "negative cursor"
		return h, nil
	}
	if cursor > head {
		h.NeedReconcile = true
		h.Reason = "cursor ahead of server"
		return h, nil
	}
	if restoreHint {
		h.NeedReconcile = true
		h.Reason = "client restore hint"
	}
	return h, nil
}

func (e *Engine) CreateCollection(ctx context.Context, kind, name, parentID string, metadata json.RawMessage) (*Collection, error) {
	if kind == "" || name == "" {
		return nil, ErrCollection
	}
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	now := store.NowMS()
	c := &Collection{
		ID:           ids.New(),
		Kind:         kind,
		Name:         name,
		ParentID:     parentID,
		Revision:     1,
		Metadata:     metadata,
		MetadataHash: ncrypto.SHA256Hex(metadata),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	var parent any
	if parentID != "" {
		parent = parentID
	}
	_, err := e.Store.DB.ExecContext(ctx, `
		INSERT INTO collections(id, kind, name, parent_id, revision, metadata, metadata_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?)
	`, c.ID, c.Kind, c.Name, parent, string(metadata), c.MetadataHash, now, now)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (e *Engine) EnsureNamedCollection(ctx context.Context, kind, name string) (*Collection, error) {
	c, err := e.FindChildCollection(ctx, kind, "", name)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return e.CreateCollection(ctx, kind, name, "", nil)
}

func (e *Engine) GetCollections(ctx context.Context, dev *identity.Device) ([]Collection, error) {
	rows, err := e.Store.DB.QueryContext(ctx, `
		SELECT id, kind, name, parent_id, revision, metadata, metadata_hash, created_at, updated_at, deleted_at
		FROM collections ORDER BY created_at
	`)
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
		if dev != nil && !dev.Permissions.CanCollection(c.ID) && !dev.Permissions.CanCollection("*") {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (e *Engine) GetChanges(ctx context.Context, dev *identity.Device, since int64, limit int, collectionID string) (ChangesResult, error) {
	head, err := e.HeadSeq(ctx)
	if err != nil {
		return ChangesResult{}, err
	}
	if since > head {
		return ChangesResult{NeedReconcile: true, NextSeq: head}, nil
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	q := `
		SELECT j.seq, j.object_id, j.collection_id, j.op, j.revision, j.content_hash, j.metadata_hash,
		       o.metadata, o.blob_id, o.size, o.deleted_at, o.kind
		FROM journal j
		JOIN objects o ON o.id = j.object_id
		WHERE j.seq > ?
	`
	args := []any{since}
	if collectionID != "" {
		q += ` AND j.collection_id = ?`
		args = append(args, collectionID)
	}
	q += ` ORDER BY j.seq LIMIT ?`
	args = append(args, limit+1)
	rows, err := e.Store.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return ChangesResult{}, err
	}
	defer rows.Close()
	var changes []Change
	for rows.Next() {
		var c Change
		var meta sql.NullString
		var blob sql.NullString
		var deleted sql.NullInt64
		var kind string
		if err := rows.Scan(&c.Seq, &c.ObjectID, &c.CollectionID, &c.Op, &c.Revision, &c.ContentHash, &c.MetadataHash,
			&meta, &blob, &c.Size, &deleted, &kind); err != nil {
			return ChangesResult{}, err
		}
		if dev != nil && !dev.Permissions.CanCollection(c.CollectionID) {
			continue
		}
		c.Kind = kind
		if meta.Valid {
			c.Metadata = json.RawMessage(meta.String)
		}
		c.BlobID = store.NullString(blob)
		c.Deleted = deleted.Valid
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return ChangesResult{}, err
	}
	res := ChangesResult{NextSeq: since}
	if len(changes) > limit {
		changes = changes[:limit]
		res.HasMore = true
	}
	res.Changes = changes
	if len(changes) > 0 {
		res.NextSeq = changes[len(changes)-1].Seq
	}
	return res, nil
}

func (e *Engine) Push(ctx context.Context, dev *identity.Device, idempotencyKey string, items []ChangeInput) ([]PushResult, error) {
	if len(items) == 0 {
		return nil, ErrBadBatch
	}
	if len(items) > 500 {
		return nil, ErrBadBatch
	}
	if idempotencyKey == "" {
		return nil, fmt.Errorf("sync: idempotency_key required")
	}
	if cached, ok, err := e.loadIdempotent(ctx, dev.ID, idempotencyKey); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}
	results := make([]PushResult, len(items))
	err := e.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		for i, in := range items {
			r, err := e.applyOne(tx, dev, idempotencyKey, in)
			if err != nil {
				return err
			}
			results[i] = r
		}
		b, err := json.Marshal(results)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO push_idempotency(key, device_id, result, created_at) VALUES (?, ?, ?, ?)`,
			idempotencyKey, dev.ID, string(b), store.NowMS())
		return err
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			cached, ok, e2 := e.loadIdempotent(ctx, dev.ID, idempotencyKey)
			if e2 == nil && ok {
				return cached, nil
			}
		}
		return nil, err
	}
	return results, nil
}

func (e *Engine) loadIdempotent(ctx context.Context, deviceID, key string) ([]PushResult, bool, error) {
	var raw string
	var owner string
	err := e.Store.DB.QueryRowContext(ctx, `SELECT device_id, result FROM push_idempotency WHERE key = ?`, key).Scan(&owner, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if owner != deviceID {
		return nil, false, ErrIdempotency
	}
	var out []PushResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (e *Engine) applyOne(tx *sql.Tx, dev *identity.Device, idem string, in ChangeInput) (PushResult, error) {
	res := PushResult{ObjectID: in.ObjectID, Status: "error"}
	if !ids.Valid(in.ObjectID) || !ids.Valid(in.CollectionID) {
		res.Error = "invalid id"
		res.Status = "error"
		return res, nil
	}
	if !dev.Permissions.CanCollection(in.CollectionID) {
		res.Error = "unauthorized"
		return res, nil
	}
	switch in.Op {
	case OpCreate, OpUpdate, OpDelete:
	default:
		res.Error = "invalid op"
		return res, nil
	}
	if in.Metadata == nil {
		in.Metadata = json.RawMessage(`{}`)
	}
	if in.Op != OpDelete {
		want := ncrypto.SHA256Hex(in.Metadata)
		if in.MetadataHash != "" && in.MetadataHash != want {
			res.Error = "metadata hash mismatch"
			return res, nil
		}
		in.MetadataHash = want
	}

	var colDeleted sql.NullInt64
	err := tx.QueryRow(`SELECT deleted_at FROM collections WHERE id = ?`, in.CollectionID).Scan(&colDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		res.Error = "collection not found"
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if colDeleted.Valid {
		res.Error = "collection deleted"
		return res, nil
	}

	obj, err := getObjectTx(tx, in.ObjectID)

	switch in.Op {
	case OpCreate:
		if err == nil {
			res.Status = "conflict"
			res.ServerRevision = obj.Revision
			res.ServerContentHash = obj.ContentHash
			return res, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return res, err
		}
		if in.BlobID != "" {
			if err := e.ensureBlob(tx, in.BlobID, in.ContentHash); err != nil {
				res.Error = err.Error()
				return res, nil
			}
		} else if in.ContentHash == "" {
			in.ContentHash = ncrypto.SHA256Hex(nil)
		}
		if err := e.ensureExtraBlobs(tx, in.Metadata); err != nil {
			res.Error = err.Error()
			return res, nil
		}
		now := store.NowMS()
		if _, err := tx.Exec(`
			INSERT INTO objects(id, collection_id, kind, revision, content_hash, metadata_hash, blob_id, size, origin_device, metadata, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		`, in.ObjectID, in.CollectionID, nonempty(in.Kind, "file"), in.ContentHash, in.MetadataHash, nullIfEmpty(in.BlobID), in.Size, dev.ID, string(in.Metadata), now, now); err != nil {
			return res, err
		}
		if err := e.Store.IncBlobRef(tx, in.BlobID); err != nil {
			return res, err
		}
		if err := e.adjustExtraBlobRefs(tx, nil, in.Metadata); err != nil {
			return res, err
		}
		seq, err := insertJournal(tx, in.ObjectID, in.CollectionID, OpCreate, 1, in.ContentHash, in.MetadataHash, dev.ID, idem)
		if err != nil {
			return res, err
		}
		if err := insertHistory(tx, in.ObjectID, 1, in.ContentHash, in.MetadataHash, in.BlobID, dev.ID, now); err != nil {
			return res, err
		}
		res.Status = "ok"
		res.Revision = 1
		res.Seq = seq
		return res, nil

	case OpUpdate:
		if errors.Is(err, ErrNotFound) {
			res.Error = "not found"
			return res, nil
		}
		if err != nil {
			return res, err
		}
		if obj.DeletedAt != nil {
			res.Status = "conflict"
			res.ServerRevision = obj.Revision
			res.ServerContentHash = obj.ContentHash
			return res, nil
		}
		if obj.Revision != in.BaseRevision && !in.Force {
			res.Status = "conflict"
			res.ServerRevision = obj.Revision
			res.ServerContentHash = obj.ContentHash
			return res, nil
		}
		if in.BlobID != "" {
			if err := e.ensureBlob(tx, in.BlobID, in.ContentHash); err != nil {
				res.Error = err.Error()
				return res, nil
			}
		}
		if err := e.ensureExtraBlobs(tx, in.Metadata); err != nil {
			res.Error = err.Error()
			return res, nil
		}
		now := store.NowMS()
		newRev := obj.Revision + 1
		oldBlob := obj.BlobID
		if _, err := tx.Exec(`
			UPDATE objects SET revision=?, content_hash=?, metadata_hash=?, blob_id=?, size=?, origin_device=?, metadata=?, updated_at=?
			WHERE id=?
		`, newRev, in.ContentHash, in.MetadataHash, nullIfEmpty(in.BlobID), in.Size, dev.ID, string(in.Metadata), now, in.ObjectID); err != nil {
			return res, err
		}
		if oldBlob != in.BlobID {
			if err := e.Store.DecBlobRef(tx, oldBlob); err != nil {
				return res, err
			}
			if err := e.Store.IncBlobRef(tx, in.BlobID); err != nil {
				return res, err
			}
		}
		if err := e.adjustExtraBlobRefs(tx, obj.Metadata, in.Metadata); err != nil {
			return res, err
		}
		seq, err := insertJournal(tx, in.ObjectID, in.CollectionID, OpUpdate, newRev, in.ContentHash, in.MetadataHash, dev.ID, idem)
		if err != nil {
			return res, err
		}
		if err := insertHistory(tx, in.ObjectID, newRev, in.ContentHash, in.MetadataHash, in.BlobID, dev.ID, now); err != nil {
			return res, err
		}
		res.Status = "ok"
		res.Revision = newRev
		res.Seq = seq
		return res, nil

	case OpDelete:
		if errors.Is(err, ErrNotFound) {
			res.Error = "not found"
			return res, nil
		}
		if err != nil {
			return res, err
		}
		if obj.DeletedAt != nil {
			res.Status = "ok"
			res.Revision = obj.Revision
			return res, nil
		}
		if obj.Revision != in.BaseRevision && !in.Force {
			res.Status = "conflict"
			res.ServerRevision = obj.Revision
			res.ServerContentHash = obj.ContentHash
			return res, nil
		}
		now := store.NowMS()
		newRev := obj.Revision + 1
		if _, err := tx.Exec(`UPDATE objects SET revision=?, deleted_at=?, updated_at=?, origin_device=? WHERE id=?`,
			newRev, now, now, dev.ID, in.ObjectID); err != nil {
			return res, err
		}
		if err := e.Store.DecBlobRef(tx, obj.BlobID); err != nil {
			return res, err
		}
		if err := e.adjustExtraBlobRefs(tx, obj.Metadata, nil); err != nil {
			return res, err
		}
		seq, err := insertJournal(tx, in.ObjectID, in.CollectionID, OpDelete, newRev, obj.ContentHash, obj.MetadataHash, dev.ID, idem)
		if err != nil {
			return res, err
		}
		res.Status = "ok"
		res.Revision = newRev
		res.Seq = seq
		return res, nil
	}
	return res, nil
}

func (e *Engine) ensureExtraBlobs(tx *sql.Tx, meta json.RawMessage) error {
	for _, h := range store.MetadataBlobIDs(meta) {
		if err := e.ensureBlob(tx, h, ""); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) adjustExtraBlobRefs(tx *sql.Tx, oldMeta, newMeta json.RawMessage) error {
	old := map[string]struct{}{}
	for _, h := range store.MetadataBlobIDs(oldMeta) {
		old[h] = struct{}{}
	}
	neu := map[string]struct{}{}
	for _, h := range store.MetadataBlobIDs(newMeta) {
		neu[h] = struct{}{}
	}
	for h := range old {
		if _, ok := neu[h]; !ok {
			if err := e.Store.DecBlobRef(tx, h); err != nil {
				return err
			}
		}
	}
	for h := range neu {
		if _, ok := old[h]; !ok {
			if err := e.Store.IncBlobRef(tx, h); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) ensureBlob(tx *sql.Tx, blobID, contentHash string) error {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM blobs WHERE id = ?`, blobID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 || !e.Store.BlobExists(blobID) {
		return ErrBlob
	}
	if contentHash != "" && contentHash != blobID {
		return fmt.Errorf("sync: content_hash must equal blob_id for blob-backed objects")
	}
	return nil
}

func (e *Engine) Ack(ctx context.Context, deviceID string, seq int64) error {
	head, err := e.HeadSeq(ctx)
	if err != nil {
		return err
	}
	if seq < 0 || seq > head {
		return ErrFutureCursor
	}
	now := store.NowMS()
	return e.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		var cur sql.NullInt64
		err := tx.QueryRow(`SELECT last_ack_seq FROM cursors WHERE device_id=? AND scope=?`, deviceID, "*").Scan(&cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cur.Valid && seq < cur.Int64 {
			return nil // do not move backwards
		}
		_, err = tx.Exec(`
			INSERT INTO cursors(device_id, scope, last_ack_seq, last_ack_at) VALUES (?, '*', ?, ?)
			ON CONFLICT(device_id, scope) DO UPDATE SET last_ack_seq=excluded.last_ack_seq, last_ack_at=excluded.last_ack_at
		`, deviceID, seq, now)
		return err
	})
}

func (e *Engine) Reconcile(ctx context.Context, dev *identity.Device, collectionID string, inv []InventoryItem) (ReconcileResult, error) {
	if !ids.Valid(collectionID) {
		return ReconcileResult{}, ErrCollection
	}
	if dev != nil && !dev.Permissions.CanCollection(collectionID) {
		return ReconcileResult{}, ErrAuthorization
	}
	rows, err := e.Store.DB.QueryContext(ctx, `
		SELECT id, revision, content_hash, deleted_at FROM objects WHERE collection_id = ?
	`, collectionID)
	if err != nil {
		return ReconcileResult{}, err
	}
	defer rows.Close()
	type srv struct {
		rev     uint64
		hash    string
		deleted bool
	}
	server := map[string]srv{}
	for rows.Next() {
		var id, hash string
		var rev uint64
		var del sql.NullInt64
		if err := rows.Scan(&id, &rev, &hash, &del); err != nil {
			return ReconcileResult{}, err
		}
		server[id] = srv{rev: rev, hash: hash, deleted: del.Valid}
	}
	if err := rows.Err(); err != nil {
		return ReconcileResult{}, err
	}
	seen := map[string]InventoryItem{}
	var out ReconcileResult
	for _, it := range inv {
		seen[it.ID] = it
		s, ok := server[it.ID]
		if !ok {
			out.MissingOnServer = append(out.MissingOnServer, it.ID)
			continue
		}
		if s.rev != it.Revision || s.hash != it.ContentHash {
			out.Mismatch = append(out.Mismatch, Mismatch{ID: it.ID, ServerRevision: s.rev, ServerContentHash: s.hash})
		}
	}
	for id := range server {
		if _, ok := seen[id]; !ok {
			out.MissingOnClient = append(out.MissingOnClient, id)
		}
	}
	return out, nil
}

func getObjectTx(tx *sql.Tx, id string) (*Object, error) {
	var o Object
	var blob sql.NullString
	var origin sql.NullString
	var deleted sql.NullInt64
	var meta string
	err := tx.QueryRow(`
		SELECT id, collection_id, kind, revision, content_hash, metadata_hash, blob_id, size, origin_device, metadata, created_at, updated_at, deleted_at
		FROM objects WHERE id = ?
	`, id).Scan(&o.ID, &o.CollectionID, &o.Kind, &o.Revision, &o.ContentHash, &o.MetadataHash, &blob, &o.Size, &origin, &meta, &o.CreatedAt, &o.UpdatedAt, &deleted)
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

func insertJournal(tx *sql.Tx, objectID, collectionID, op string, rev uint64, ch, mh, deviceID, idem string) (int64, error) {
	r, err := tx.Exec(`
		INSERT INTO journal(object_id, collection_id, op, revision, content_hash, metadata_hash, device_id, ts, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, objectID, collectionID, op, rev, ch, mh, deviceID, store.NowMS(), idem)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func insertHistory(tx *sql.Tx, objectID string, rev uint64, ch, mh, blob, deviceID string, ts int64) error {
	_, err := tx.Exec(`
		INSERT INTO object_history(object_id, revision, content_hash, metadata_hash, blob_id, device_id, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, objectID, rev, ch, mh, nullIfEmpty(blob), deviceID, ts)
	return err
}

func scanCollection(rows *sql.Rows) (Collection, error) {
	var c Collection
	var parent sql.NullString
	var meta string
	var deleted sql.NullInt64
	err := rows.Scan(&c.ID, &c.Kind, &c.Name, &parent, &c.Revision, &meta, &c.MetadataHash, &c.CreatedAt, &c.UpdatedAt, &deleted)
	c.ParentID = store.NullString(parent)
	c.Metadata = json.RawMessage(meta)
	c.DeletedAt = store.NullInt64(deleted)
	return c, err
}

func nonempty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
