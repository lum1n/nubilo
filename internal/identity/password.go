package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
)

var (
	ErrBadPassword = errors.New("identity: invalid credentials")
	ErrLocked      = errors.New("identity: too many failed password attempts")
)

// CreateDAVDevice creates a protocol-only device and returns the plaintext app password once.
func (s *Service) CreateDAVDevice(ctx context.Context, name, scope string) (*Device, string, error) {
	name, err := SanitizeName(name)
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
		Name:        name,
		Role:        RoleDAV,
		Permissions: DefaultPermissions(RoleDAV),
		CreatedAt:   now,
		LastSeen:    &now,
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
		if _, err := tx.Exec(`
			INSERT INTO devices(id, name, public_key, role, permissions, created_at, last_seen)
			VALUES (?, ?, NULL, ?, ?, ?, ?)
		`, dev.ID, dev.Name, string(dev.Role), string(perm), now, now); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO app_passwords(id, device_id, hash, salt, scope, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, pwID, dev.ID, hash, salt, scope, now)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return dev, plain, nil
}

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
	now := s.now().UnixMilli()
	_, _ = s.Store.DB.ExecContext(ctx, `UPDATE app_passwords SET last_used = ? WHERE device_id = ? AND revoked_at IS NULL`, now, deviceID)
	s.Touch(ctx, deviceID)
	return dev, nil
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
