package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
	"nubilo/internal/store"
)

var (
	ErrNotFound      = errors.New("identity: not found")
	ErrRevoked       = errors.New("identity: device revoked")
	ErrInvalidCode   = errors.New("identity: invalid pairing code")
	ErrExpired       = errors.New("identity: pairing expired")
	ErrTooManyTries  = errors.New("identity: too many pairing attempts")
	ErrTooManyActive = errors.New("identity: too many active pairing sessions")
	ErrCompleted     = errors.New("identity: pairing already completed")
	ErrBadSignature  = errors.New("identity: bad pairing signature")
	ErrName          = errors.New("identity: invalid device name")
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleAgent  Role = "agent"
	RoleClient Role = "client"
	RoleDAV    Role = "dav"
)

type Permissions struct {
	Role        Role     `json:"role"`
	Collections []string `json:"collections"` // "*" or ULIDs
	Protocols   []string `json:"protocols"`   // sync, caldav, carddav, webdav, api
	Admin       bool     `json:"admin"`
}

func DefaultPermissions(role Role) Permissions {
	p := Permissions{Role: role, Collections: []string{"*"}}
	switch role {
	case RoleAdmin:
		p.Admin = true
		p.Protocols = []string{"sync", "api"}
	case RoleAgent, RoleClient:
		p.Protocols = []string{"sync", "api"}
	case RoleDAV:
		p.Collections = []string{"*"}
		p.Protocols = []string{"caldav", "carddav", "webdav"}
	}
	return p
}

func (p Permissions) HasProtocol(proto string) bool {
	for _, x := range p.Protocols {
		if x == proto || x == "*" {
			return true
		}
	}
	return false
}

func (p Permissions) CanCollection(id string) bool {
	for _, x := range p.Collections {
		if x == "*" || x == id {
			return true
		}
	}
	return false
}

type Device struct {
	ID          string
	Name        string
	PublicKey   []byte
	Role        Role
	Permissions Permissions
	CreatedAt   int64
	LastSeen    *int64
	RevokedAt   *int64
}

func (d *Device) Revoked() bool {
	return d.RevokedAt != nil
}

type PairingSession struct {
	ID        string
	ExpiresAt int64
	Attempts  int
}

type Service struct {
	Store       *store.Store
	TTL         time.Duration
	MaxAttempts int
	MaxActive   int
	now         func() time.Time
}

func NewService(st *store.Store) *Service {
	return &Service{
		Store:       st,
		TTL:         5 * time.Minute,
		MaxAttempts: 5,
		MaxActive:   3,
		now:         time.Now,
	}
}

func SanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", ErrName
	}
	if strings.ContainsRune(name, 0) {
		return "", ErrName
	}
	return name, nil
}

func (s *Service) StartPairing(ctx context.Context, role Role) (code string, sess PairingSession, err error) {
	if role == "" {
		role = RoleClient
	}
	now := s.now()
	var active int
	err = s.Store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pairing_sessions
		WHERE completed_at IS NULL AND expires_at > ?
	`, now.UnixMilli()).Scan(&active)
	if err != nil {
		return "", sess, err
	}
	if active >= s.MaxActive {
		return "", sess, ErrTooManyActive
	}
	code, err = ncrypto.PairingCode()
	if err != nil {
		return "", sess, err
	}
	salt, err := ncrypto.NewSalt()
	if err != nil {
		return "", sess, err
	}
	hash := ncrypto.HashSecret([]byte(code), salt)
	sess.ID = ids.New()
	sess.ExpiresAt = now.Add(s.TTL).UnixMilli()
	_, err = s.Store.DB.ExecContext(ctx, `
		INSERT INTO pairing_sessions(id, code_hash, code_salt, created_at, expires_at, attempts, pending_role)
		VALUES (?, ?, ?, ?, ?, 0, ?)
	`, sess.ID, hash, salt, now.UnixMilli(), sess.ExpiresAt, string(role))
	if err != nil {
		return "", sess, err
	}
	return code, sess, nil
}

type BeginRequest struct {
	Code      string
	Name      string
	PublicKey []byte
	Nonce     []byte
}

type BeginResult struct {
	PairingID string
	Challenge []byte
	ServerID  string
}

func (s *Service) Begin(ctx context.Context, req BeginRequest) (BeginResult, error) {
	var zero BeginResult
	code := ncrypto.NormalizePairingCode(req.Code)
	if len(code) != 10 {
		return zero, ErrInvalidCode
	}
	name, err := SanitizeName(req.Name)
	if err != nil {
		return zero, err
	}
	if len(req.PublicKey) != 32 {
		return zero, fmt.Errorf("identity: public key must be 32 bytes")
	}
	now := s.now().UnixMilli()
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT id, code_hash, code_salt, expires_at, attempts, completed_at
		FROM pairing_sessions
		WHERE completed_at IS NULL AND expires_at > ?
	`, now)
	if err != nil {
		return zero, err
	}
	defer rows.Close()

	type row struct {
		id      string
		hash    []byte
		salt    []byte
		expires int64
		tries   int
	}
	var found *row
	for rows.Next() {
		var r row
		var completed sql.NullInt64
		if err := rows.Scan(&r.id, &r.hash, &r.salt, &r.expires, &r.tries, &completed); err != nil {
			return zero, err
		}
		if ncrypto.VerifySecret([]byte(code), r.salt, r.hash) == nil {
			found = &r
			break
		}
	}
	if err := rows.Err(); err != nil {
		return zero, err
	}
	if found == nil {
		return zero, ErrInvalidCode
	}
	if found.expires <= now {
		return zero, ErrExpired
	}
	if found.tries >= s.MaxAttempts {
		return zero, ErrTooManyTries
	}

	challenge, err := ncrypto.Random(32)
	if err != nil {
		return zero, err
	}
	_, err = s.Store.DB.ExecContext(ctx, `
		UPDATE pairing_sessions
		SET attempts = attempts + 1,
		    challenge = ?,
		    pending_pubkey = ?,
		    pending_name = ?
		WHERE id = ?
	`, challenge, req.PublicKey, name, found.id)
	if err != nil {
		return zero, err
	}
	if found.tries+1 >= s.MaxAttempts {
		// still allow this attempt to complete; next begin will fail
	}
	return BeginResult{PairingID: found.id, Challenge: challenge}, nil
}

func (s *Service) Complete(ctx context.Context, pairingID string, signature []byte) (*Device, error) {
	now := s.now().UnixMilli()
	var (
		hash, salt, challenge, pub []byte
		expires                    int64
		tries                      int
		completed                  sql.NullInt64
		name, role                 sql.NullString
	)
	err := s.Store.DB.QueryRowContext(ctx, `
		SELECT code_hash, code_salt, expires_at, attempts, completed_at, challenge, pending_pubkey, pending_name, pending_role
		FROM pairing_sessions WHERE id = ?
	`, pairingID).Scan(&hash, &salt, &expires, &tries, &completed, &challenge, &pub, &name, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if completed.Valid {
		return nil, ErrCompleted
	}
	if expires <= now {
		return nil, ErrExpired
	}
	if tries > s.MaxAttempts {
		return nil, ErrTooManyTries
	}
	if len(challenge) == 0 || len(pub) == 0 || !name.Valid {
		return nil, ErrInvalidCode
	}
	if !ncrypto.VerifyEd25519(pub, challenge, signature) {
		_, _ = s.Store.DB.ExecContext(ctx, `UPDATE pairing_sessions SET attempts = attempts + 1 WHERE id = ?`, pairingID)
		return nil, ErrBadSignature
	}
	r := Role(role.String)
	if r == "" {
		r = RoleClient
	}
	dev := &Device{
		ID:          ids.New(),
		Name:        name.String,
		PublicKey:   append([]byte(nil), pub...),
		Role:        r,
		Permissions: DefaultPermissions(r),
		CreatedAt:   now,
		LastSeen:    &now,
	}
	perm, err := json.Marshal(dev.Permissions)
	if err != nil {
		return nil, err
	}
	err = s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO devices(id, name, public_key, role, permissions, created_at, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, dev.ID, dev.Name, dev.PublicKey, string(dev.Role), string(perm), dev.CreatedAt, now); err != nil {
			return err
		}
		_, err := tx.Exec(`
			UPDATE pairing_sessions
			SET completed_at = ?, device_id = ?, code_hash = x'', challenge = NULL
			WHERE id = ?
		`, now, dev.ID, pairingID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return dev, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Device, error) {
	d, err := s.scanDevice(ctx, `SELECT id, name, public_key, role, permissions, created_at, last_seen, revoked_at FROM devices WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) List(ctx context.Context) ([]Device, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT id, name, public_key, role, permissions, created_at, last_seen, revoked_at
		FROM devices ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDeviceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Service) Revoke(ctx context.Context, id string) error {
	now := s.now().UnixMilli()
	res, err := s.Store.DB.ExecContext(ctx, `
		UPDATE devices SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?
	`, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.Store.DB.ExecContext(ctx, `UPDATE app_passwords SET revoked_at = COALESCE(revoked_at, ?) WHERE device_id = ?`, now, id)
	_, _ = s.Store.DB.ExecContext(ctx, `DELETE FROM nonces WHERE device_id = ?`, id)
	return nil
}

func (s *Service) Rename(ctx context.Context, id, name string) error {
	name, err := SanitizeName(name)
	if err != nil {
		return err
	}
	res, err := s.Store.DB.ExecContext(ctx, `UPDATE devices SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) RotatePublicKey(ctx context.Context, id string, pub []byte) error {
	if len(pub) != 32 {
		return fmt.Errorf("identity: public key must be 32 bytes")
	}
	res, err := s.Store.DB.ExecContext(ctx, `UPDATE devices SET public_key = ? WHERE id = ? AND revoked_at IS NULL`, pub, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.Store.DB.ExecContext(ctx, `DELETE FROM nonces WHERE device_id = ?`, id)
	return nil
}

func (s *Service) Enroll(ctx context.Context, name string, pub []byte, role Role) (*Device, error) {
	name, err := SanitizeName(name)
	if err != nil {
		return nil, err
	}
	if len(pub) != 32 {
		return nil, fmt.Errorf("identity: public key must be 32 bytes")
	}
	if role == "" {
		role = RoleClient
	}
	now := s.now().UnixMilli()
	dev := &Device{
		ID:          ids.New(),
		Name:        name,
		PublicKey:   append([]byte(nil), pub...),
		Role:        role,
		Permissions: DefaultPermissions(role),
		CreatedAt:   now,
		LastSeen:    &now,
	}
	perm, err := json.Marshal(dev.Permissions)
	if err != nil {
		return nil, err
	}
	_, err = s.Store.DB.ExecContext(ctx, `
		INSERT INTO devices(id, name, public_key, role, permissions, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, dev.ID, dev.Name, dev.PublicKey, string(dev.Role), string(perm), now, now)
	if err != nil {
		return nil, err
	}
	return dev, nil
}

func (s *Service) Touch(ctx context.Context, id string) {
	now := s.now().UnixMilli()
	_, _ = s.Store.DB.ExecContext(ctx, `
		UPDATE devices SET last_seen = ? WHERE id = ? AND (last_seen IS NULL OR last_seen < ?)
	`, now, id, now-60_000)
}

func (s *Service) scanDevice(ctx context.Context, q string, arg any) (*Device, error) {
	row := s.Store.DB.QueryRowContext(ctx, q, arg)
	d, err := scanDeviceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDeviceRow(sc scanner) (*Device, error) {
	var d Device
	var pub []byte
	var perm string
	var last, rev sql.NullInt64
	var role string
	if err := sc.Scan(&d.ID, &d.Name, &pub, &role, &perm, &d.CreatedAt, &last, &rev); err != nil {
		return nil, err
	}
	d.PublicKey = pub
	d.Role = Role(role)
	d.LastSeen = store.NullInt64(last)
	d.RevokedAt = store.NullInt64(rev)
	if perm != "" {
		_ = json.Unmarshal([]byte(perm), &d.Permissions)
	}
	return &d, nil
}
