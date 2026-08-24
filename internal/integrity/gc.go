package integrity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"nubilo/internal/store"
)

// Report is a dry-run or applied garbage-collection summary.
type Report struct {
	MinAckSeq             int64    `json:"min_ack_seq"`
	UnreferencedBlobs     []string `json:"unreferenced_blobs,omitempty"`
	CompactableTombstones []string `json:"compactable_tombstones,omitempty"`
	BlobsRemoved          int      `json:"blobs_removed"`
	TombstonesCompacted   int      `json:"tombstones_compacted"`
	DryRun                bool     `json:"dry_run"`
}

// Collect removes unreferenced blobs and compactable tombstones.
// Tombstones are compactable only when every non-revoked sync device
// has ACKed past their delete journal seq (devices with no cursor are treated as 0).
func Collect(ctx context.Context, st *store.Store, apply bool) (Report, error) {
	rep := Report{DryRun: !apply}
	minAck, err := minSyncAck(ctx, st)
	if err != nil {
		return rep, err
	}
	rep.MinAckSeq = minAck

	tombstones, err := compactableTombstones(ctx, st, minAck)
	if err != nil {
		return rep, err
	}
	rep.CompactableTombstones = tombstones

	live, err := LiveBlobRefs(ctx, st)
	if err != nil {
		return rep, err
	}
	bRows, err := st.DB.QueryContext(ctx, `SELECT id FROM blobs`)
	if err != nil {
		return rep, err
	}
	defer bRows.Close()
	var unused []string
	for bRows.Next() {
		var id string
		if err := bRows.Scan(&id); err != nil {
			return rep, err
		}
		if live[id] == 0 {
			unused = append(unused, id)
		}
	}
	if err := bRows.Err(); err != nil {
		return rep, err
	}
	rep.UnreferencedBlobs = unused

	if !apply {
		return rep, nil
	}

	for _, id := range unused {
		if err := st.RemoveBlob(ctx, id); err != nil {
			return rep, err
		}
		rep.BlobsRemoved++
	}
	n, err := deleteTombstones(ctx, st, tombstones)
	if err != nil {
		return rep, err
	}
	rep.TombstonesCompacted = n
	return rep, nil
}

func minSyncAck(ctx context.Context, st *store.Store) (int64, error) {
	rows, err := st.DB.QueryContext(ctx, `SELECT id, permissions FROM devices WHERE revoked_at IS NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, perm string
		if err := rows.Scan(&id, &perm); err != nil {
			return 0, err
		}
		if deviceHasSync(perm) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		var head sql.NullInt64
		if err := st.DB.QueryRowContext(ctx, `SELECT MAX(seq) FROM journal`).Scan(&head); err != nil {
			return 0, err
		}
		if !head.Valid {
			return 0, nil
		}
		return head.Int64, nil
	}
	var minAck int64
	first := true
	for _, id := range ids {
		var seq sql.NullInt64
		err := st.DB.QueryRowContext(ctx, `SELECT last_ack_seq FROM cursors WHERE device_id=? AND scope='*'`, id).Scan(&seq)
		ack := int64(0)
		if err == nil && seq.Valid {
			ack = seq.Int64
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if first || ack < minAck {
			minAck = ack
			first = false
		}
	}
	return minAck, nil
}

func deviceHasSync(permJSON string) bool {
	var p struct {
		Protocols []string `json:"protocols"`
	}
	if err := json.Unmarshal([]byte(permJSON), &p); err != nil {
		return false
	}
	if len(p.Protocols) == 0 {
		return true
	}
	for _, x := range p.Protocols {
		if x == "sync" || x == "*" {
			return true
		}
	}
	return false
}

func compactableTombstones(ctx context.Context, st *store.Store, minAck int64) ([]string, error) {
	rows, err := st.DB.QueryContext(ctx, `SELECT id FROM objects WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		var seq sql.NullInt64
		if err := st.DB.QueryRowContext(ctx, `SELECT MAX(seq) FROM journal WHERE object_id=?`, id).Scan(&seq); err != nil {
			return nil, err
		}
		if seq.Valid && seq.Int64 <= minAck {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func deleteTombstones(ctx context.Context, st *store.Store, ids []string) (int, error) {
	n := 0
	err := st.WithWrite(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM journal WHERE object_id=?`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM object_history WHERE object_id=?`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM objects WHERE id=? AND deleted_at IS NOT NULL`, id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
