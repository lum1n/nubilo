package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
)

var (
	ErrBadPassword      = errors.New("identity: invalid credentials")
	ErrLocked           = errors.New("identity: too many failed password attempts")
	ErrTooManyPasswords = errors.New("identity: too many app passwords")
)

// MaxAppPasswords caps active app passwords per user. Basic auth by
// username verifies each one with Argon2, so the cap bounds that cost.
const MaxAppPasswords = 10

// AppPassword describes one app password (a protocol-only DAV device).
type AppPassword struct {
	ID        string `json:"id"` // device id; revoke with RevokeAppPassword
	Label     string `json:"label"`
	Scope     string `json:"scope"`
	CreatedAt int64  `json:"created_at"`
	LastUsed  *int64 `json:"last_used,omitempty"`
}

// CreateAppPassword creates a protocol-only device for userID ("" = default
// owner) and returns the plaintext password once. Basic auth accepts either
// the user's username or the returned device ID as the username.
func (s *Service) CreateAppPassword(ctx context.Context, userID, label, scope string) (*Device, string, error) {
	label, err := SanitizeName(label)
	if err != nil {
		return nil, "", err
	}
	u, err := s.resolveUser(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	scope = normalizeScope(scope)
	if scope == "" {
		scope = "webdav"
	}
	plain, err := ncrypto.AppPassword()
	if err != nil {
		return nil, "", err
	}
	salt, err := ncrypto.NewSalt()
	if err != nil {
		return nil, "", err
	}
	hash := ncrypto.HashSecret([]byte(plain), salt)
	now := s.now().UnixMilli()
	dev := &Device{
		ID:          ids.New(),
		Name:        label,
		Role:        RoleDAV,
		Permissions: DefaultPermissions(RoleDAV),
		CreatedAt:   now,
		LastSeen:    &now,
		UserID:      u.ID,
		UserRole:    u.Role,
		UserStatus:  u.Status,
	}
	if scope != "caldav,carddav,webdav,photos" {
		dev.Permissions.Protocols = strings.Split(scope, ",")
	}
	perm, err := json.Marshal(dev.Permissions)
	if err != nil {
		return nil, "", err
	}
	pwID := ids.New()
	err = s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		var active int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM app_passwords WHERE user_id = ? AND revoked_at IS NULL`, u.ID).Scan(&active); err != nil {
			return err
		}
		if active >= MaxAppPasswords {
			return ErrTooManyPasswords
		}
		if _, err := tx.Exec(`
			INSERT INTO devices(id, name, public_key, role, permissions, created_at, last_seen, user_id)
			VALUES (?, ?, NULL, ?, ?, ?, ?, ?)
		`, dev.ID, dev.Name, string(dev.Role), string(perm), now, now, u.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO app_passwords(id, device_id, hash, salt, scope, created_at, user_id, label)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, pwID, dev.ID, hash, salt, scope, now, u.ID, label)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return dev, plain, nil
}

// ListAppPasswords returns the active app passwords of userID.
func (s *Service) ListAppPasswords(ctx context.Context, userID string) ([]AppPassword, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT p.device_id, p.label, p.scope, p.created_at, p.last_used
		FROM app_passwords p JOIN devices d ON d.id = p.device_id
		WHERE p.user_id = ? AND p.revoked_at IS NULL AND d.revoked_at IS NULL
		ORDER BY p.created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppPassword
	for rows.Next() {
		var p AppPassword
		var last sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Label, &p.Scope, &p.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			v := last.Int64
			p.LastUsed = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RevokeAppPassword revokes an app password (its DAV device) owned by userID.
func (s *Service) RevokeAppPassword(ctx context.Context, userID, deviceID string) error {
	d, err := s.Get(ctx, deviceID)
	if err != nil {
		return err
	}
	if d.UserID != userID || d.Role != RoleDAV {
		return ErrNotFound
	}
	return s.Revoke(ctx, deviceID)
}

// AuthenticatePassword verifies the app password of one DAV device.
func (s *Service) AuthenticatePassword(ctx context.Context, deviceID, password string) (*Device, error) {
	if deviceID == "" || password == "" {
		return nil, ErrBadPassword
	}
	dev, err := s.Get(ctx, deviceID)
	if err != nil {
		return nil, ErrBadPassword
	}
	if dev.Revoked() {
		return nil, ErrRevoked
	}
	var hash, salt []byte
	var revoked sql.NullInt64
	err = s.Store.DB.QueryRowContext(ctx, `
		SELECT hash, salt, revoked_at FROM app_passwords
		WHERE device_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC LIMIT 1
	`, deviceID).Scan(&hash, &salt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadPassword
	}
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		return nil, ErrRevoked
	}
	if err := ncrypto.VerifySecret([]byte(password), salt, hash); err != nil {
		return nil, ErrBadPassword
	}
	s.markPasswordUsed(ctx, deviceID)
	return dev, nil
}

func (s *Service) markPasswordUsed(ctx context.Context, deviceID string) {
	now := s.now().UnixMilli()
	_, _ = s.Store.DB.ExecContext(ctx, `
		UPDATE app_passwords SET last_used = ?
		WHERE device_id = ? AND revoked_at IS NULL AND (last_used IS NULL OR last_used < ?)
	`, now, deviceID, now-60_000)
	s.Touch(ctx, deviceID)
}

// AuthenticateBasic resolves HTTP Basic credentials. A ULID username is a
// legacy per-device login; anything else is a username whose active app
// passwords are tried in turn. Successes are cached for ten minutes because
// Apple clients send Basic credentials on every request.
func (s *Service) AuthenticateBasic(ctx context.Context, username, password string) (*Device, error) {
	if username == "" || password == "" {
		return nil, ErrBadPassword
	}
	key := basicKey(username, password)
	if devID, ok := s.basic.get(key, s.now()); ok {
		dev, err := s.Get(ctx, devID)
		if err == nil && !dev.Revoked() {
			s.markPasswordUsed(ctx, dev.ID)
			return dev, nil
		}
		s.basic.forget(key)
	}
	var dev *Device
	var err error
	if ids.Valid(username) {
		dev, err = s.AuthenticatePassword(ctx, username, password)
	} else {
		dev, err = s.authenticateUserPassword(ctx, username, password)
	}
	if err != nil {
		return nil, err
	}
	s.basic.put(key, dev.ID, s.now())
	return dev, nil
}

// CachedBasic returns the device for credentials that recently succeeded,
// without Argon2 work. Callers use it to let a known-good client through
// while its username is locked by someone else's failed guesses.
func (s *Service) CachedBasic(ctx context.Context, username, password string) (*Device, bool) {
	if username == "" || password == "" {
		return nil, false
	}
	devID, ok := s.basic.get(basicKey(username, password), s.now())
	if !ok {
		return nil, false
	}
	dev, err := s.Get(ctx, devID)
	if err != nil || dev.Revoked() {
		return nil, false
	}
	return dev, true
}

func (s *Service) authenticateUserPassword(ctx context.Context, username, password string) (*Device, error) {
	u, err := s.GetUserByName(ctx, username)
	if err != nil {
		return nil, ErrBadPassword
	}
	if !u.Active() {
		return nil, ErrRevoked
	}
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT p.device_id, p.hash, p.salt
		FROM app_passwords p JOIN devices d ON d.id = p.device_id
		WHERE p.user_id = ? AND p.revoked_at IS NULL AND d.revoked_at IS NULL
		ORDER BY p.created_at DESC LIMIT ?
	`, u.ID, MaxAppPasswords)
	if err != nil {
		return nil, err
	}
	type cand struct {
		dev        string
		hash, salt []byte
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.dev, &c.hash, &c.salt); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range cands {
		if ncrypto.VerifySecret([]byte(password), c.salt, c.hash) == nil {
			dev, err := s.Get(ctx, c.dev)
			if err != nil {
				return nil, ErrBadPassword
			}
			if dev.Revoked() {
				return nil, ErrRevoked
			}
			s.markPasswordUsed(ctx, dev.ID)
			return dev, nil
		}
	}
	return nil, ErrBadPassword
}

func basicKey(username, password string) [32]byte {
	return sha256.Sum256([]byte(strings.ToLower(username) + "\x00" + password))
}

const basicCacheTTL = 10 * time.Minute

type basicEntry struct {
	deviceID string
	expires  time.Time
}

type basicCache struct {
	mu sync.Mutex
	m  map[[32]byte]basicEntry
}

func newBasicCache() *basicCache {
	return &basicCache{m: map[[32]byte]basicEntry{}}
}

func (c *basicCache) get(k [32]byte, now time.Time) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || now.After(e.expires) {
		delete(c.m, k)
		return "", false
	}
	return e.deviceID, true
}

func (c *basicCache) put(k [32]byte, deviceID string, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 4096 {
		for key, e := range c.m {
			if now.After(e.expires) {
				delete(c.m, key)
			}
		}
	}
	c.m[k] = basicEntry{deviceID: deviceID, expires: now.Add(basicCacheTTL)}
}

func (c *basicCache) forget(k [32]byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

func (c *basicCache) forgetDevice(deviceID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for k, e := range c.m {
		if e.deviceID == deviceID {
			delete(c.m, k)
		}
	}
	c.mu.Unlock()
}

func normalizeScope(scope string) string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	scope = strings.ReplaceAll(scope, " ", "")
	if scope == "" || scope == "all" || scope == "*" {
		return "caldav,carddav,webdav,photos"
	}
	parts := strings.Split(scope, ",")
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		switch p {
		case "webdav", "caldav", "carddav", "photos":
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return strings.Join(out, ",")
}
