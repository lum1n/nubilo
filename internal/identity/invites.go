package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
)

var (
	ErrInviteInvalid = errors.New("identity: invalid invite")
	ErrInviteUsed    = errors.New("identity: invite already used")
)

const (
	invitePrefix  = "nbi-"
	inviteCodeLen = 20
	crockfordABC  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

type Invite struct {
	ID        string   `json:"id"`
	Role      UserRole `json:"role"`
	UserID    string   `json:"user_id,omitempty"`
	MaxUses   int      `json:"max_uses"`
	Uses      int      `json:"uses"`
	ExpiresAt int64    `json:"expires_at"`
	CreatedBy string   `json:"created_by"`
	CreatedAt int64    `json:"created_at"`
}

type RedeemRequest struct {
	Username    string
	DisplayName string
	DeviceName  string
	PublicKey   []byte
}

type RedeemResult struct {
	BeginResult
	User    *User
	NewUser bool
}

// NewInviteCode returns "nbi-" followed by 20 Crockford base32 characters (100 bits).
func NewInviteCode() (string, error) {
	raw, err := ncrypto.Random(13)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(invitePrefix)
	var acc uint32
	bits := 0
	for _, x := range raw {
		acc = acc<<8 | uint32(x)
		bits += 8
		for bits >= 5 && b.Len() < len(invitePrefix)+inviteCodeLen {
			bits -= 5
			b.WriteByte(crockfordABC[(acc>>uint(bits))&31])
		}
	}
	return b.String(), nil
}

// NormalizeInviteCode returns the 20-character body of an invite code, or "".
func NormalizeInviteCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) >= len(invitePrefix) && strings.EqualFold(code[:len(invitePrefix)], invitePrefix) {
		code = code[len(invitePrefix):]
	}
	out := make([]byte, 0, inviteCodeLen)
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c == '-' || c == ' ':
			continue
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		}
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if !strings.ContainsRune(crockfordABC, rune(c)) {
			return ""
		}
		out = append(out, c)
	}
	if len(out) != inviteCodeLen {
		return ""
	}
	return string(out)
}

// CreateInvite creates an invite. With forUserID empty, redeeming creates a
// new user with role; otherwise the redeemer pairs a device for forUserID.
func (s *Service) CreateInvite(ctx context.Context, createdBy string, role UserRole, forUserID string, ttl time.Duration, maxUses int) (string, *Invite, error) {
	if role == "" {
		role = UserMember
	}
	if !validUserRole(role) {
		return "", nil, ErrUserRole
	}
	if forUserID != "" {
		u, err := s.GetUser(ctx, forUserID)
		if err != nil {
			return "", nil, err
		}
		if !u.Active() {
			return "", nil, ErrUserDisabled
		}
		role = u.Role
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if ttl > 30*24*time.Hour {
		ttl = 30 * 24 * time.Hour
	}
	if maxUses <= 0 {
		maxUses = 1
	}
	if maxUses > 50 {
		maxUses = 50
	}
	code, err := NewInviteCode()
	if err != nil {
		return "", nil, err
	}
	salt, err := ncrypto.NewSalt()
	if err != nil {
		return "", nil, err
	}
	hash := ncrypto.HashSecret([]byte(NormalizeInviteCode(code)), salt)
	now := s.now()
	inv := &Invite{
		ID: ids.New(), Role: role, UserID: forUserID, MaxUses: maxUses,
		ExpiresAt: now.Add(ttl).UnixMilli(), CreatedBy: createdBy, CreatedAt: now.UnixMilli(),
	}
	_, err = s.Store.DB.ExecContext(ctx, `
		INSERT INTO invites(id, code_hash, code_salt, role, user_id, max_uses, uses, expires_at, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
	`, inv.ID, hash, salt, string(inv.Role), nullString(forUserID), inv.MaxUses, inv.ExpiresAt, createdBy, inv.CreatedAt)
	if err != nil {
		return "", nil, err
	}
	return code, inv, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const inviteCols = `id, role, COALESCE(user_id, ''), max_uses, uses, expires_at, created_by, created_at`

func scanInvite(sc scanner) (*Invite, error) {
	var inv Invite
	var role string
	if err := sc.Scan(&inv.ID, &role, &inv.UserID, &inv.MaxUses, &inv.Uses, &inv.ExpiresAt, &inv.CreatedBy, &inv.CreatedAt); err != nil {
		return nil, err
	}
	inv.Role = UserRole(role)
	return &inv, nil
}

// ListInvites returns invites that can still be redeemed.
func (s *Service) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT `+inviteCols+` FROM invites WHERE uses < max_uses AND expires_at > ? ORDER BY created_at
	`, s.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

func (s *Service) RevokeInvite(ctx context.Context, id string) error {
	res, err := s.Store.DB.ExecContext(ctx, `DELETE FROM invites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) findInvite(ctx context.Context, code string) (*Invite, error) {
	norm := NormalizeInviteCode(code)
	if norm == "" {
		return nil, ErrInviteInvalid
	}
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT code_hash, code_salt, `+inviteCols+` FROM invites WHERE uses < max_uses AND expires_at > ?
	`, s.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash, salt []byte
		var inv Invite
		var role string
		if err := rows.Scan(&hash, &salt, &inv.ID, &role, &inv.UserID, &inv.MaxUses, &inv.Uses, &inv.ExpiresAt, &inv.CreatedBy, &inv.CreatedAt); err != nil {
			return nil, err
		}
		inv.Role = UserRole(role)
		if ncrypto.VerifySecret([]byte(norm), salt, hash) == nil {
			return &inv, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrInviteInvalid
}

// RedeemInvite consumes one use of an invite, creates the user when the
// invite is not bound to one, and opens a pairing session that is already
// past Begin. The caller signs Challenge and calls Complete.
func (s *Service) RedeemInvite(ctx context.Context, code string, req RedeemRequest) (*RedeemResult, error) {
	devName, err := SanitizeName(req.DeviceName)
	if err != nil {
		return nil, err
	}
	if len(req.PublicKey) != 32 {
		return nil, fmt.Errorf("identity: public key must be 32 bytes")
	}
	inv, err := s.findInvite(ctx, code)
	if err != nil {
		return nil, err
	}
	challenge, err := ncrypto.Random(32)
	if err != nil {
		return nil, err
	}
	salt, err := ncrypto.NewSalt()
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := &RedeemResult{}
	err = s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE invites SET uses = uses + 1 WHERE id = ? AND uses < max_uses AND expires_at > ?`, inv.ID, now.UnixMilli())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrInviteUsed
		}
		var u *User
		if inv.UserID == "" {
			u, err = createUserTx(tx, req.Username, req.DisplayName, inv.Role, now.UnixMilli(), false)
			if err != nil {
				return err
			}
			out.NewUser = true
		} else {
			u, err = scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, inv.UserID))
			if err != nil {
				return err
			}
			if !u.Active() {
				return ErrUserDisabled
			}
		}
		out.User = u
		out.PairingID = ids.New()
		out.Challenge = challenge
		_, err = tx.Exec(`
			INSERT INTO pairing_sessions(id, code_hash, code_salt, created_at, expires_at, attempts,
				challenge, pending_pubkey, pending_name, pending_role, user_id)
			VALUES (?, x'', ?, ?, ?, 1, ?, ?, ?, ?, ?)
		`, out.PairingID, salt, now.UnixMilli(), now.Add(s.TTL).UnixMilli(), challenge, req.PublicKey, devName, string(RoleClient), u.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.NewUser {
		if err := s.userCreated(ctx, out.User); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Upload tokens let a device upload blobs from a background transfer that
// cannot be signed at send time (iOS background URLSession). They are valid
// only for blob PUT and the photo check/commit endpoints.

var ErrUploadToken = errors.New("identity: invalid upload token")

const uploadTokenPrefix = "nbu_"

// CreateUploadToken issues a bearer token bound to deviceID.
func (s *Service) CreateUploadToken(ctx context.Context, deviceID string, ttl time.Duration) (string, int64, error) {
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = 24 * time.Hour
	}
	secret, err := ncrypto.Random(32)
	if err != nil {
		return "", 0, err
	}
	id := ids.New()
	sum := sha256.Sum256(secret)
	now := s.now()
	exp := now.Add(ttl).UnixMilli()
	_, err = s.Store.DB.ExecContext(ctx, `
		INSERT INTO upload_tokens(id, device_id, hash, expires_at) VALUES (?, ?, ?, ?)
	`, id, deviceID, sum[:], exp)
	if err != nil {
		return "", 0, err
	}
	_, _ = s.Store.DB.ExecContext(ctx, `DELETE FROM upload_tokens WHERE expires_at < ?`, now.UnixMilli())
	return uploadTokenPrefix + id + "_" + base64.RawURLEncoding.EncodeToString(secret), exp, nil
}

// AuthenticateUploadToken returns the device an upload token belongs to.
func (s *Service) AuthenticateUploadToken(ctx context.Context, token string) (*Device, error) {
	rest, ok := strings.CutPrefix(token, uploadTokenPrefix)
	if !ok {
		return nil, ErrUploadToken
	}
	id, enc, ok := strings.Cut(rest, "_")
	if !ok || !ids.Valid(id) {
		return nil, ErrUploadToken
	}
	secret, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(secret) != 32 {
		return nil, ErrUploadToken
	}
	var devID string
	var hash []byte
	var exp int64
	err = s.Store.DB.QueryRowContext(ctx, `SELECT device_id, hash, expires_at FROM upload_tokens WHERE id = ?`, id).Scan(&devID, &hash, &exp)
	if err != nil {
		return nil, ErrUploadToken
	}
	sum := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 || exp <= s.now().UnixMilli() {
		return nil, ErrUploadToken
	}
	dev, err := s.Get(ctx, devID)
	if err != nil || dev.Revoked() {
		return nil, ErrUploadToken
	}
	return dev, nil
}
