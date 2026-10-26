package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"nubilo/internal/ids"
	"nubilo/internal/store"
)

var (
	ErrUsername     = errors.New("identity: invalid username")
	ErrUserExists   = errors.New("identity: username taken")
	ErrUserDisabled = errors.New("identity: user disabled")
	ErrUserRole     = errors.New("identity: invalid user role")
	ErrLastAdmin    = errors.New("identity: cannot remove the last active admin")
)

type UserRole string

const (
	UserAdmin  UserRole = "admin"
	UserMember UserRole = "member"
)

const (
	UserActive   = "active"
	UserDisabled = "disabled"
)

type User struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        UserRole `json:"role"`
	Status      string   `json:"status"`
	CreatedAt   int64    `json:"created_at"`
}

func (u *User) Active() bool { return u != nil && u.Status == UserActive }

func (u *User) Admin() bool { return u != nil && u.Role == UserAdmin }

// NormalizeUsername lowercases and validates a username: 1–32 chars of
// [a-z0-9._-], starting with a letter or digit, and never a ULID (ULID
// Basic-auth usernames are reserved for legacy per-device app passwords).
func NormalizeUsername(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(name) > 32 {
		return "", ErrUsername
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '.' || r == '_' || r == '-') && i > 0:
		default:
			return "", ErrUsername
		}
	}
	if ids.Valid(strings.ToUpper(name)) {
		return "", ErrUsername
	}
	if reservedUsernames[name] {
		return "", ErrUsername
	}
	return name, nil
}

// reservedUsernames cannot be chosen by users. "owner" is the default admin
// that CLI pairing and the admin console fall back to; letting a member take
// it would route the operator's devices and uploads into their account.
var reservedUsernames = map[string]bool{store.OwnerUsername: true, "admin": true, "root": true, "system": true}

func validUserRole(r UserRole) bool {
	return r == UserAdmin || r == UserMember
}

func (s *Service) CreateUser(ctx context.Context, username, displayName string, role UserRole) (*User, error) {
	return s.createUser(ctx, username, displayName, role, false)
}

func (s *Service) createUser(ctx context.Context, username, displayName string, role UserRole, reservedOK bool) (*User, error) {
	var u *User
	err := s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		var err error
		u, err = createUserTx(tx, username, displayName, role, s.now().UnixMilli(), reservedOK)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.userCreated(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func createUserTx(tx *sql.Tx, username, displayName string, role UserRole, now int64, reservedOK bool) (*User, error) {
	name, err := NormalizeUsername(username)
	if reservedOK && errors.Is(err, ErrUsername) && reservedUsernames[strings.ToLower(strings.TrimSpace(username))] {
		name, err = strings.ToLower(strings.TrimSpace(username)), nil
	}
	if err != nil {
		return nil, err
	}
	if role == "" {
		role = UserMember
	}
	if !validUserRole(role) {
		return nil, ErrUserRole
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = name
	}
	if _, err := SanitizeName(displayName); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, name).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrUserExists
	}
	u := &User{ID: ids.New(), Username: name, DisplayName: displayName, Role: role, Status: UserActive, CreatedAt: now}
	_, err = tx.Exec(`
		INSERT INTO users(id, username, display_name, role, status, created_at) VALUES (?, ?, ?, ?, ?, ?)
	`, u.ID, u.Username, u.DisplayName, string(u.Role), u.Status, u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) userCreated(ctx context.Context, u *User) error {
	if s.OnUserCreated == nil {
		return nil
	}
	return s.OnUserCreated(ctx, u)
}

const userCols = `id, username, display_name, role, status, created_at`

func scanUser(sc scanner) (*User, error) {
	var u User
	var role string
	if err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &role, &u.Status, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Role = UserRole(role)
	return &u, nil
}

func (s *Service) GetUser(ctx context.Context, id string) (*User, error) {
	return scanUser(s.Store.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Service) GetUserByName(ctx context.Context, username string) (*User, error) {
	return scanUser(s.Store.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, strings.ToLower(strings.TrimSpace(username))))
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SetUserStatus enables or disables a user. Disabling also revokes every
// device and app password of that user so sessions end immediately.
func (s *Service) SetUserStatus(ctx context.Context, id, status string) error {
	if status != UserActive && status != UserDisabled {
		return ErrUserRole
	}
	now := s.now().UnixMilli()
	return s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		if status == UserDisabled {
			if err := ensureOtherAdminTx(tx, id); err != nil {
				return err
			}
		}
		res, err := tx.Exec(`UPDATE users SET status = ? WHERE id = ?`, status, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if status == UserDisabled {
			if _, err := tx.Exec(`UPDATE devices SET revoked_at = COALESCE(revoked_at, ?) WHERE user_id = ?`, now, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE app_passwords SET revoked_at = COALESCE(revoked_at, ?) WHERE user_id = ?`, now, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) SetUserRole(ctx context.Context, id string, role UserRole) error {
	if !validUserRole(role) {
		return ErrUserRole
	}
	return s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		if role != UserAdmin {
			if err := ensureOtherAdminTx(tx, id); err != nil {
				return err
			}
		}
		res, err := tx.Exec(`UPDATE users SET role = ? WHERE id = ?`, string(role), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Service) SetDisplayName(ctx context.Context, id, displayName string) error {
	name, err := SanitizeName(displayName)
	if err != nil {
		return err
	}
	res, err := s.Store.DB.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ensureOtherAdminTx fails when id is the only active admin.
func ensureOtherAdminTx(tx *sql.Tx, id string) error {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active' AND id <> ?`, id).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		var isAdmin int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ? AND role = 'admin' AND status = 'active'`, id).Scan(&isAdmin); err != nil {
			return err
		}
		if isAdmin > 0 {
			return ErrLastAdmin
		}
	}
	return nil
}

// EnsureOwner returns the default admin: the user named "owner" when present,
// else the oldest active admin, else a newly created "owner" admin.
func (s *Service) EnsureOwner(ctx context.Context) (*User, error) {
	if u, err := s.GetUserByName(ctx, store.OwnerUsername); err == nil && u.Active() && u.Admin() {
		return u, nil
	}
	u, err := scanUser(s.Store.DB.QueryRowContext(ctx, `
		SELECT `+userCols+` FROM users WHERE role = 'admin' AND status = 'active' ORDER BY created_at LIMIT 1
	`))
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	u, err = s.createUser(ctx, store.OwnerUsername, "Owner", UserAdmin, true)
	if errors.Is(err, ErrUserExists) {
		// Lost a race with another creator; use it only if it is an admin.
		if u, err := s.GetUserByName(ctx, store.OwnerUsername); err == nil && u.Admin() && u.Active() {
			return u, nil
		}
		return nil, ErrUserExists
	}
	return u, err
}

// resolveUser maps "" to the default owner and checks the user is active.
func (s *Service) resolveUser(ctx context.Context, userID string) (*User, error) {
	if userID == "" {
		return s.EnsureOwner(ctx)
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !u.Active() {
		return nil, ErrUserDisabled
	}
	return u, nil
}

// ListUserDevices returns devices that belong to userID.
func (s *Service) ListUserDevices(ctx context.Context, userID string) ([]Device, error) {
	return s.listDevices(ctx, `WHERE d.user_id = ?`, userID)
}
