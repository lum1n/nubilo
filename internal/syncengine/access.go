package syncengine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
	"nubilo/internal/store"
)

var ErrShare = errors.New("sync: invalid share")

// Scope selects which collections a query may see: everything (system
// tooling) or what one user owns or has been shared.
type Scope struct {
	UserID string
	all    bool
}

// ScopeAll sees every collection. Only for local system tooling.
func ScopeAll() Scope { return Scope{all: true} }

// UserScope sees collections userID owns or that are shared with userID.
// An empty userID sees nothing.
func UserScope(userID string) Scope { return Scope{UserID: userID} }

// ScopeOf returns the scope of an authenticated device: system devices see
// everything, user devices see their user's collections.
func ScopeOf(dev *identity.Device) Scope {
	if dev != nil && dev.System() {
		return ScopeAll()
	}
	if dev == nil {
		return Scope{}
	}
	return UserScope(dev.UserID)
}

func (s Scope) All() bool { return s.all }

// shareMatchSQL matches shares (alias s) made to one user directly or to a
// group they belong to. It takes the user ID twice.
const shareMatchSQL = `((s.principal_type = 'user' AND s.principal_id = ?)
	OR (s.principal_type = 'group' AND s.principal_id IN (SELECT gm.group_id FROM group_members gm WHERE gm.user_id = ?)))`

// shareAccessSQL is the access a share grants: 2 for rw, 1 for ro.
const shareAccessSQL = `CASE s.access WHEN 'rw' THEN 2 ELSE 1 END`

// accessFilterSQL limits rows of alias c (collections) to those visible to
// one user. It takes the user ID three times (userArgs).
const accessFilterSQL = `(c.owner_user_id = ? OR (EXISTS (
	SELECT 1 FROM shares s WHERE s.collection_id = COALESCE(c.root_id, c.id) AND ` + shareMatchSQL + `)
	AND EXISTS (SELECT 1 FROM users ou WHERE ou.id = c.owner_user_id AND ou.status = 'active')))`

// accessExprSQL evaluates to the authz.Access of one user on alias c: the
// best of their direct and group shares. It takes the user ID three times.
const accessExprSQL = `(CASE WHEN c.owner_user_id = ? THEN 3 ELSE COALESCE((
	SELECT MAX(` + shareAccessSQL + `) FROM shares s JOIN users ou ON ou.id = c.owner_user_id AND ou.status = 'active'
	WHERE s.collection_id = COALESCE(c.root_id, c.id) AND ` + shareMatchSQL + `), 0) END)`

// userArgs repeats userID for accessFilterSQL and accessExprSQL.
func userArgs(userID string) []any { return []any{userID, userID, userID} }

// filter returns a WHERE fragment over alias c (collections) and its args.
func (s Scope) filter() (string, []any) {
	if s.all {
		return "1=1", nil
	}
	return accessFilterSQL, userArgs(s.UserID)
}

// accessExpr returns a SELECT expression for the caller's authz.Access on alias c.
func (s Scope) accessExpr() (string, []any) {
	if s.all {
		return "3", nil
	}
	return accessExprSQL, userArgs(s.UserID)
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

type ctxQuerier struct {
	ctx context.Context
	db  *sql.DB
}

func (c ctxQuerier) QueryRow(q string, args ...any) *sql.Row {
	return c.db.QueryRowContext(c.ctx, q, args...)
}

// Access returns userID's access to a collection. Disabled users have none.
func (e *Engine) Access(ctx context.Context, userID, collectionID string) (authz.Access, error) {
	return accessQ(ctxQuerier{ctx, e.Store.DB}, userID, collectionID)
}

// AccessFor returns a device's access to a collection (system devices own everything).
func (e *Engine) AccessFor(ctx context.Context, dev *identity.Device, collectionID string) (authz.Access, error) {
	return accessForQ(ctxQuerier{ctx, e.Store.DB}, dev, collectionID)
}

func accessForQ(q querier, dev *identity.Device, collectionID string) (authz.Access, error) {
	if dev == nil || dev.Revoked() {
		return authz.AccessNone, nil
	}
	if dev.System() {
		var n int
		if err := q.QueryRow(`SELECT COUNT(*) FROM collections WHERE id = ?`, collectionID).Scan(&n); err != nil {
			return authz.AccessNone, err
		}
		if n == 0 {
			return authz.AccessNone, ErrNotFound
		}
		return authz.AccessOwner, nil
	}
	return accessQ(q, dev.UserID, collectionID)
}

func accessQ(q querier, userID, collectionID string) (authz.Access, error) {
	if userID == "" {
		return authz.AccessNone, nil
	}
	var owner, root string
	err := q.QueryRow(`SELECT COALESCE(owner_user_id, ''), COALESCE(root_id, id) FROM collections WHERE id = ?`, collectionID).Scan(&owner, &root)
	if errors.Is(err, sql.ErrNoRows) {
		return authz.AccessNone, ErrNotFound
	}
	if err != nil {
		return authz.AccessNone, err
	}
	var status string
	err = q.QueryRow(`SELECT status FROM users WHERE id = ?`, userID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != identity.UserActive) {
		return authz.AccessNone, nil
	}
	if err != nil {
		return authz.AccessNone, err
	}
	if owner == userID {
		return authz.AccessOwner, nil
	}
	// A disabled owner's shares go dark with them.
	var ownerStatus string
	if err := q.QueryRow(`SELECT status FROM users WHERE id = ?`, owner).Scan(&ownerStatus); err != nil || ownerStatus != identity.UserActive {
		return authz.AccessNone, nil
	}
	var best sql.NullInt64
	err = q.QueryRow(`SELECT MAX(`+shareAccessSQL+`) FROM shares s WHERE s.collection_id = ? AND `+shareMatchSQL,
		root, userID, userID).Scan(&best)
	if err != nil {
		return authz.AccessNone, err
	}
	switch best.Int64 {
	case 2:
		return authz.AccessWrite, nil
	case 1:
		return authz.AccessRead, nil
	}
	return authz.AccessNone, nil
}

// CollectionsRev changes whenever the set of collections visible in scope,
// their revisions, or the caller's access to them change.
func (e *Engine) CollectionsRev(ctx context.Context, sc Scope) (string, error) {
	cols, err := e.GetCollections(ctx, sc)
	if err != nil {
		return "", err
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].ID < cols[j].ID })
	h := sha256.New()
	for _, c := range cols {
		fmt.Fprintf(h, "%s:%d:%s;", c.ID, c.Revision, c.Access)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

type Share struct {
	CollectionID  string `json:"collection_id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Username      string `json:"username,omitempty"`
	GroupName     string `json:"group_name,omitempty"`
	Access        string `json:"access"`
	CreatedBy     string `json:"created_by"`
	CreatedAt     int64  `json:"created_at"`
}

// SetShare grants (or changes) a user's or group's access to a root collection.
func (e *Engine) SetShare(ctx context.Context, collectionID, principalType, principalID, access, createdBy string) error {
	if principalType != "user" && principalType != "group" {
		return ErrShare
	}
	if access != "ro" && access != "rw" {
		return ErrShare
	}
	c, err := e.GetCollection(ctx, collectionID)
	if err != nil {
		return err
	}
	if c.DeletedAt != nil || c.ParentID != "" {
		return ErrShare
	}
	if principalType == "user" && principalID == c.OwnerUserID {
		return ErrShare
	}
	table := "users"
	if principalType == "group" {
		table = "groups"
	}
	var n int
	if err := e.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ?`, principalID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrShare
	}
	return e.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO shares(collection_id, principal_type, principal_id, access, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(collection_id, principal_type, principal_id) DO UPDATE SET access = excluded.access
		`, collectionID, principalType, principalID, access, createdBy, store.NowMS()); err != nil {
			return err
		}
		return touchCollectionTx(tx, collectionID)
	})
}

// RemoveShare revokes a share. Missing shares are not an error.
func (e *Engine) RemoveShare(ctx context.Context, collectionID, principalType, principalID string) error {
	return e.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM shares WHERE collection_id = ? AND principal_type = ? AND principal_id = ?`,
			collectionID, principalType, principalID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return touchCollectionTx(tx, collectionID)
		}
		return nil
	})
}

// touchCollectionTx bumps a collection revision so collections_rev changes for
// everyone who sees it.
func touchCollectionTx(tx *sql.Tx, id string) error {
	_, err := tx.Exec(`UPDATE collections SET revision = revision + 1, updated_at = ? WHERE id = ?`, store.NowMS(), id)
	return err
}

func (e *Engine) ListShares(ctx context.Context, collectionID string) ([]Share, error) {
	rows, err := e.Store.DB.QueryContext(ctx, `
		SELECT s.collection_id, s.principal_type, s.principal_id, COALESCE(u.username, ''), COALESCE(g.name, ''), s.access, s.created_by, s.created_at
		FROM shares s LEFT JOIN users u ON s.principal_type = 'user' AND u.id = s.principal_id
		LEFT JOIN groups g ON s.principal_type = 'group' AND g.id = s.principal_id
		WHERE s.collection_id = ? ORDER BY s.created_at
	`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var s Share
		if err := rows.Scan(&s.CollectionID, &s.PrincipalType, &s.PrincipalID, &s.Username, &s.GroupName, &s.Access, &s.CreatedBy, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Pref is one user's display preference for a collection they do not own.
type Pref struct {
	Color string
	Order int
}

func (e *Engine) GetPref(ctx context.Context, userID, collectionID string) (Pref, error) {
	var p Pref
	var color sql.NullString
	var order sql.NullInt64
	err := e.Store.DB.QueryRowContext(ctx, `SELECT color, sort_order FROM collection_prefs WHERE user_id = ? AND collection_id = ?`,
		userID, collectionID).Scan(&color, &order)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	p.Color = color.String
	p.Order = int(order.Int64)
	return p, err
}

// SetPref stores a non-empty color and a non-zero order; zero values keep the existing value.
func (e *Engine) SetPref(ctx context.Context, userID, collectionID string, p Pref) error {
	var color, order any
	if p.Color != "" {
		color = p.Color
	}
	if p.Order != 0 {
		order = p.Order
	}
	_, err := e.Store.DB.ExecContext(ctx, `
		INSERT INTO collection_prefs(user_id, collection_id, color, sort_order) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, collection_id) DO UPDATE SET
			color = COALESCE(excluded.color, collection_prefs.color),
			sort_order = COALESCE(excluded.sort_order, collection_prefs.sort_order)
	`, userID, collectionID, color, order)
	return err
}

// BlobReadable reports whether userID may read a blob: they hold a ref, or a
// live object referencing it sits in a collection they can read.
func (e *Engine) BlobReadable(ctx context.Context, userID, hash string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	ok, err := e.Store.HasBlobRef(ctx, userID, hash)
	if err != nil || ok {
		return ok, err
	}
	var n int
	err = e.Store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM objects o JOIN collections c ON c.id = o.collection_id
		WHERE o.deleted_at IS NULL AND c.deleted_at IS NULL
		  AND (o.blob_id = ? OR EXISTS (
			SELECT 1 FROM json_each(CASE WHEN json_valid(o.metadata) THEN o.metadata ELSE '{}' END) m
			WHERE lower(m.key) IN ('preview_hash', 'thumb_hash', 'live_pair_hash') AND lower(m.value) = ?))
		  AND `+accessFilterSQL+`
		LIMIT 1
	`, append([]any{hash, hash}, userArgs(userID)...)...).Scan(&n)
	return n > 0, err
}

// ErrMirrorReadOnly is the push error for a write to a mirrored collection.
const ErrMirrorReadOnly = "read-only mirror"

// mirrorReadOnlyQ reports whether dev may not write to collectionID because
// it is a mirror: collection metadata "mirror": true, set by the Mac app
// that copies a calendar from the Mac (for example an Exchange account only
// that Mac reaches). Only the device recorded in "mirror_device" (see
// MirrorPatch) and system devices write to it, so phones see the copy but
// cannot change the original's events. This guards against accidental
// edits; the owner can still clear the flag.
func mirrorReadOnlyQ(q querier, dev *identity.Device, collectionID string) (bool, error) {
	if dev == nil || dev.System() {
		return false, nil
	}
	var mirror sql.NullBool
	var owner sql.NullString
	err := q.QueryRow(`
		SELECT json_extract(metadata, '$.mirror'), json_extract(metadata, '$.mirror_device') FROM collections
		WHERE id = ? AND json_valid(metadata)
	`, collectionID).Scan(&mirror, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return mirror.Valid && mirror.Bool && owner.String != dev.ID, nil
}

// MirrorPatch records dev as the writer of a collection when a metadata
// patch turns "mirror" on, overriding any "mirror_device" the client sent.
// Other patches are returned unchanged.
func MirrorPatch(patch json.RawMessage, dev *identity.Device) json.RawMessage {
	var m map[string]any
	if dev == nil || json.Unmarshal(patch, &m) != nil {
		return patch
	}
	if on, _ := m["mirror"].(bool); !on {
		delete(m, "mirror_device")
		if b, err := json.Marshal(m); err == nil {
			return b
		}
		return patch
	}
	m["mirror_device"] = dev.ID
	b, err := json.Marshal(m)
	if err != nil {
		return patch
	}
	return b
}
