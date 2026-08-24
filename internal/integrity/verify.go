package integrity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/store"
)

type Issue struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Ref     string `json:"ref,omitempty"`
}

func (i Issue) String() string {
	if i.Ref == "" {
		return i.Kind + ": " + i.Message
	}
	return i.Kind + ": " + i.Message + " (" + i.Ref + ")"
}

type objRow struct {
	id, blob, coll, meta string
	rev                  uint64
	deleted              bool
}

// LiveBlobRefs counts blob uses from live (non-tombstoned) objects:
// objects.blob_id plus metadata preview_hash / thumb_hash. This is the
// source of truth for GC; blobs.refcount should match.
func LiveBlobRefs(ctx context.Context, st *store.Store) (map[string]int, error) {
	rows, err := st.DB.QueryContext(ctx, `SELECT blob_id, metadata, deleted_at FROM objects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]int{}
	for rows.Next() {
		var blob sql.NullString
		var meta string
		var deleted sql.NullInt64
		if err := rows.Scan(&blob, &meta, &deleted); err != nil {
			return nil, err
		}
		if deleted.Valid {
			continue
		}
		if h := store.NullString(blob); h != "" {
			refs[h]++
		}
		for _, h := range store.MetadataBlobIDs(json.RawMessage(meta)) {
			refs[h]++
		}
	}
	return refs, rows.Err()
}

func Check(ctx context.Context, st *store.Store) ([]Issue, error) {
	var issues []Issue

	rows, err := st.DB.QueryContext(ctx, `SELECT id, blob_id, collection_id, revision, deleted_at, metadata FROM objects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []objRow
	for rows.Next() {
		var o objRow
		var blob sql.NullString
		var deleted sql.NullInt64
		if err := rows.Scan(&o.id, &blob, &o.coll, &o.rev, &deleted, &o.meta); err != nil {
			return nil, err
		}
		o.blob = store.NullString(blob)
		o.deleted = deleted.Valid
		objects = append(objects, o)
		var n int
		if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections WHERE id=?`, o.coll).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			issues = append(issues, Issue{Kind: "invalid_reference", Message: "object collection missing", Ref: o.id})
		}
		var jrev sql.NullInt64
		_ = st.DB.QueryRowContext(ctx, `SELECT MAX(revision) FROM journal WHERE object_id=?`, o.id).Scan(&jrev)
		if !jrev.Valid || uint64(jrev.Int64) != o.rev {
			issues = append(issues, Issue{Kind: "inconsistent_revision", Message: "object revision does not match journal", Ref: o.id})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	checkHash := func(hash string) {
		if hash == "" {
			return
		}
		if !st.BlobExists(hash) {
			issues = append(issues, Issue{Kind: "missing_blob", Message: "referenced blob file missing", Ref: hash})
			return
		}
		pt, err := st.GetBlobPlaintext(hash)
		if err != nil {
			issues = append(issues, Issue{Kind: "hash_mismatch", Message: "blob decrypt/hash failed", Ref: hash})
			return
		}
		if ncrypto.SHA256Hex(pt) != hash {
			issues = append(issues, Issue{Kind: "hash_mismatch", Message: "plaintext hash mismatch", Ref: hash})
		}
	}

	seen := map[string]struct{}{}
	for _, o := range objects {
		if o.deleted {
			continue
		}
		if o.blob != "" {
			seen[o.blob] = struct{}{}
		}
		for _, h := range store.MetadataBlobIDs(json.RawMessage(o.meta)) {
			seen[h] = struct{}{}
		}
	}

	live, err := LiveBlobRefs(ctx, st)
	if err != nil {
		return nil, err
	}

	onDisk, err := st.ListBlobFiles()
	if err != nil {
		return nil, err
	}
	known := map[string]struct{}{}
	bRows, err := st.DB.QueryContext(ctx, `SELECT id, refcount FROM blobs`)
	if err != nil {
		return nil, err
	}
	defer bRows.Close()
	for bRows.Next() {
		var id string
		var rc int
		if err := bRows.Scan(&id, &rc); err != nil {
			return nil, err
		}
		known[id] = struct{}{}
		checkHash(id)
		if n := live[id]; n != rc {
			issues = append(issues, Issue{Kind: "refcount", Message: fmt.Sprintf("refcount %d live %d", rc, n), Ref: id})
		}
	}
	if err := bRows.Err(); err != nil {
		return nil, err
	}
	for h := range seen {
		if _, ok := known[h]; !ok {
			issues = append(issues, Issue{Kind: "missing_blob", Message: "referenced blob has no metadata row", Ref: h})
		}
	}

	for _, h := range onDisk {
		if _, ok := known[h]; !ok {
			issues = append(issues, Issue{Kind: "orphan_blob", Message: "blob file has no metadata row", Ref: h})
		}
	}

	jRows, err := st.DB.QueryContext(ctx, `SELECT seq, object_id FROM journal`)
	if err != nil {
		return nil, err
	}
	defer jRows.Close()
	for jRows.Next() {
		var seq int64
		var oid string
		if err := jRows.Scan(&seq, &oid); err != nil {
			return nil, err
		}
		var n int
		_ = st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects WHERE id=?`, oid).Scan(&n)
		if n == 0 {
			issues = append(issues, Issue{Kind: "invalid_reference", Message: "journal points at missing object", Ref: fmt.Sprintf("seq:%d", seq)})
		}
	}

	var head sql.NullInt64
	_ = st.DB.QueryRowContext(ctx, `SELECT MAX(seq) FROM journal`).Scan(&head)
	cRows, err := st.DB.QueryContext(ctx, `SELECT device_id, last_ack_seq FROM cursors`)
	if err != nil {
		return nil, err
	}
	defer cRows.Close()
	for cRows.Next() {
		var did string
		var seq int64
		if err := cRows.Scan(&did, &seq); err != nil {
			return nil, err
		}
		if head.Valid && seq > head.Int64 {
			issues = append(issues, Issue{Kind: "cursor", Message: "cursor past head", Ref: did})
		}
	}

	dRows, err := st.DB.QueryContext(ctx, `SELECT id, public_key FROM devices`)
	if err != nil {
		return nil, err
	}
	defer dRows.Close()
	for dRows.Next() {
		var id string
		var pub []byte
		if err := dRows.Scan(&id, &pub); err != nil {
			return nil, err
		}
		if pub != nil && len(pub) != 32 {
			issues = append(issues, Issue{Kind: "device_key", Message: "malformed public key", Ref: id})
		}
	}

	return issues, nil
}

func RepairOrphans(ctx context.Context, st *store.Store) (int, error) {
	issues, err := Check(ctx, st)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, i := range issues {
		if i.Kind == "orphan_blob" && i.Ref != "" {
			if err := os.Remove(st.BlobPath(i.Ref)); err == nil {
				n++
			}
		}
	}
	return n, nil
}

func RepairRefcounts(ctx context.Context, st *store.Store) (int, error) {
	live, err := LiveBlobRefs(ctx, st)
	if err != nil {
		return 0, err
	}
	rows, err := st.DB.QueryContext(ctx, `SELECT id, refcount FROM blobs`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		var rc int
		if err := rows.Scan(&id, &rc); err != nil {
			return 0, err
		}
		want := live[id]
		if want == rc {
			continue
		}
		if _, err := st.DB.ExecContext(ctx, `UPDATE blobs SET refcount = ? WHERE id = ?`, want, id); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func Repair(ctx context.Context, st *store.Store) (orphans, refs int, err error) {
	orphans, err = RepairOrphans(ctx, st)
	if err != nil {
		return orphans, 0, err
	}
	refs, err = RepairRefcounts(ctx, st)
	return orphans, refs, err
}
