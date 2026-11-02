package syncengine

import (
	"database/sql"
	"errors"
	"math"

	"nubilo/internal/store"
)

// ErrQuotaExceeded is the push error for a write that would take the
// collection owner past their storage quota (users.quota_bytes).
const ErrQuotaExceeded = "quota exceeded"

// MaxMetadataBytes caps an object's metadata; metadata counts toward quota.
const MaxMetadataBytes = 256 << 10

// blobSize returns the stored plaintext size of a blob, or 0 if unknown.
func blobSize(tx *sql.Tx, blobID string) (int64, error) {
	if blobID == "" {
		return 0, nil
	}
	var n int64
	err := tx.QueryRow(`SELECT size FROM blobs WHERE id = ?`, blobID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// charge validates an object's size and returns what it costs its owner:
// its bytes, its metadata, and every extra blob its metadata names (photo
// previews, thumbnails, Live Photo movies). A blob-backed object's size is
// always its stored blob size, so clients cannot under- or over-report it.
// It returns a push error message ("" when fine).
func (e *Engine) charge(tx *sql.Tx, in *ChangeInput) (int64, string, error) {
	if len(in.Metadata) > MaxMetadataBytes {
		return 0, "metadata too large", nil
	}
	if in.BlobID != "" {
		n, err := blobSize(tx, in.BlobID)
		if err != nil {
			return 0, "", err
		}
		in.Size = n
	} else if in.Size < 0 || in.Size > e.Store.MaxBlob {
		return 0, "invalid size", nil
	}
	total := in.Size + int64(len(in.Metadata))
	seen := map[string]bool{in.BlobID: true}
	for _, h := range store.MetadataBlobIDs(in.Metadata) {
		if seen[h] {
			continue
		}
		seen[h] = true
		n, err := blobSize(tx, h)
		if err != nil {
			return 0, "", err
		}
		total += n
	}
	return total, "", nil
}

// quotaAllows reports whether owner may grow by delta bytes. Usage is the
// sum of objects.charged over live objects in collections the owner holds,
// whoever wrote them (a write into a shared folder counts against its
// owner). Shrinking writes are always allowed so a user over quota can still
// clean up.
func quotaAllows(tx *sql.Tx, owner string, delta int64) (bool, error) {
	if owner == "" || delta <= 0 {
		return true, nil
	}
	var quota int64
	err := tx.QueryRow(`SELECT quota_bytes FROM users WHERE id = ?`, owner).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil || quota <= 0 {
		return err == nil, err
	}
	used, err := ownerUsage(tx, owner)
	if err != nil {
		return false, err
	}
	// Written so it cannot overflow: used and delta are both bounded by sane
	// sizes, but never trust that.
	return used <= quota && delta <= quota-used, nil
}

func ownerUsage(q interface {
	QueryRow(string, ...any) *sql.Row
}, owner string) (int64, error) {
	// TOTAL is a float and never overflows; clamp back to int64.
	var used float64
	err := q.QueryRow(`
		SELECT TOTAL(o.charged)
		FROM objects o JOIN collections c ON c.id = o.collection_id
		WHERE c.owner_user_id = ? AND o.deleted_at IS NULL AND c.deleted_at IS NULL
	`, owner).Scan(&used)
	if used >= math.MaxInt64 {
		return math.MaxInt64, err
	}
	return int64(used), err
}
