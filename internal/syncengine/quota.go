package syncengine

import (
	"database/sql"
	"errors"
)

// ErrQuotaExceeded is the push error for a write that would take the
// collection owner past their storage quota (users.quota_bytes).
const ErrQuotaExceeded = "quota exceeded"

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

// quotaAllows reports whether owner may grow by delta bytes. Usage is the
// same figure UserUsage reports: the size of live objects in collections the
// owner holds, whoever wrote them (a write into a shared folder counts against
// its owner). Shrinking writes are always allowed so a user over quota can
// still clean up.
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
	return used+delta <= quota, nil
}

func ownerUsage(q interface {
	QueryRow(string, ...any) *sql.Row
}, owner string) (int64, error) {
	var used int64
	err := q.QueryRow(`
		SELECT COALESCE(SUM(o.size), 0)
		FROM objects o JOIN collections c ON c.id = o.collection_id
		WHERE c.owner_user_id = ? AND o.deleted_at IS NULL AND c.deleted_at IS NULL
	`, owner).Scan(&used)
	return used, err
}

// fixSize stops a client from under-reporting the size of a blob-backed
// object to dodge its quota: the stored blob size wins when larger.
func fixSize(tx *sql.Tx, in *ChangeInput) error {
	n, err := blobSize(tx, in.BlobID)
	if err != nil {
		return err
	}
	if n > in.Size {
		in.Size = n
	}
	if in.Size < 0 {
		in.Size = 0
	}
	return nil
}
