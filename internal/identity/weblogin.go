package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
)

// Web sign-in works like a TV login: the browser asks for a short code,
// shows it with a QR code, and a paired app approves it. The browser holds a
// secret (cookie) that is bound to the pending login, so knowing the code is
// not enough to receive the session. A session then acts as the device that
// approved it: same user, same access, and it ends when that device is
// revoked or the user is disabled.

var (
	ErrWebLogin   = errors.New("identity: invalid or expired web sign-in")
	ErrWebSession = errors.New("identity: invalid web session")
)

const (
	WebLoginTTL       = 5 * time.Minute
	WebSessionIdle    = 7 * 24 * time.Hour
	WebSessionMaxLife = 30 * 24 * time.Hour
	webSessionPrefix  = "nbw_"
	webLoginPrefix    = "nbl_"
	// No 0/O, 1/I/L: the code may be typed into the app.
	webCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

type WebLogin struct {
	ID         string `json:"-"`
	Code       string `json:"code"`
	UserAgent  string `json:"user_agent"`
	IP         string `json:"ip"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	ApprovedBy string `json:"-"`
}

type WebSession struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	DeviceID  string `json:"device_id"`
	UserAgent string `json:"user_agent"`
	IP        string `json:"ip"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	ExpiresAt int64  `json:"expires_at"`
}

// NormalizeWebCode accepts "abcd-efgh", "ABCD EFGH" or "ABCDEFGH".
func NormalizeWebCode(code string) string {
	code = strings.ToUpper(code)
	var b strings.Builder
	for _, r := range code {
		if strings.ContainsRune(webCodeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) != 8 {
		return ""
	}
	return s[:4] + "-" + s[4:]
}

func webCode() (string, error) {
	raw, err := ncrypto.Random(8)
	if err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, c := range raw {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, webCodeAlphabet[int(c)%len(webCodeAlphabet)])
	}
	return string(out), nil
}

// StartWebLogin creates a pending sign-in. It returns the login (with the
// code to show) and the browser secret to keep in a cookie.
func (s *Service) StartWebLogin(ctx context.Context, userAgent, ip string) (*WebLogin, string, error) {
	secret, err := ncrypto.Random(32)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(secret)
	now := s.now()
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	l := &WebLogin{ID: ids.New(), UserAgent: userAgent, IP: ip, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(WebLoginTTL).UnixMilli()}
	_, _ = s.Store.DB.ExecContext(ctx, `DELETE FROM web_logins WHERE expires_at < ?`, now.UnixMilli())
	for range 5 {
		if l.Code, err = webCode(); err != nil {
			return nil, "", err
		}
		_, err = s.Store.DB.ExecContext(ctx, `
			INSERT INTO web_logins(id, code, browser_hash, user_agent, ip, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, l.ID, l.Code, sum[:], l.UserAgent, l.IP, l.CreatedAt, l.ExpiresAt)
		if err == nil {
			return l, webLoginPrefix + l.ID + "_" + base64.RawURLEncoding.EncodeToString(secret), nil
		}
	}
	return nil, "", err
}

// PendingWebLogin returns an unexpired, unapproved login by code, for the
// approving app to show where the browser is.
func (s *Service) PendingWebLogin(ctx context.Context, code string) (*WebLogin, error) {
	code = NormalizeWebCode(code)
	if code == "" {
		return nil, ErrWebLogin
	}
	var l WebLogin
	var approved sql.NullString
	err := s.Store.DB.QueryRowContext(ctx, `
		SELECT id, code, user_agent, ip, created_at, expires_at, approved_by FROM web_logins WHERE code = ?
	`, code).Scan(&l.ID, &l.Code, &l.UserAgent, &l.IP, &l.CreatedAt, &l.ExpiresAt, &approved)
	if err != nil || approved.Valid || l.ExpiresAt <= s.now().UnixMilli() {
		return nil, ErrWebLogin
	}
	return &l, nil
}

// ApproveWebLogin lets dev's user sign in in the browser waiting on code.
// Only app devices of an active user may approve.
func (s *Service) ApproveWebLogin(ctx context.Context, dev *Device, code string) error {
	if dev == nil || dev.Revoked() || dev.UserID == "" || dev.Role != RoleClient {
		return ErrWebLogin
	}
	l, err := s.PendingWebLogin(ctx, code)
	if err != nil {
		return err
	}
	res, err := s.Store.DB.ExecContext(ctx, `
		UPDATE web_logins SET approved_by = ?, approved_at = ? WHERE id = ? AND approved_by IS NULL
	`, dev.ID, s.now().UnixMilli(), l.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrWebLogin
	}
	return nil
}

func splitSecret(token, prefix string) (string, []byte, bool) {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return "", nil, false
	}
	id, enc, ok := strings.Cut(rest, "_")
	if !ok || !ids.Valid(id) {
		return "", nil, false
	}
	secret, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(secret) != 32 {
		return "", nil, false
	}
	return id, secret, true
}

// PollWebLogin is called by the browser with its secret. It returns
// ("", nil) while waiting, and once approved turns the login into a session
// and returns the session token (only once).
func (s *Service) PollWebLogin(ctx context.Context, browserSecret string) (string, *WebSession, error) {
	id, secret, ok := splitSecret(browserSecret, webLoginPrefix)
	if !ok {
		return "", nil, ErrWebLogin
	}
	var token string
	var sess *WebSession
	err := s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		var hash []byte
		var ua, ip string
		var exp int64
		var approved sql.NullString
		err := tx.QueryRow(`SELECT browser_hash, user_agent, ip, expires_at, approved_by FROM web_logins WHERE id = ?`, id).
			Scan(&hash, &ua, &ip, &exp, &approved)
		if err != nil {
			return ErrWebLogin
		}
		sum := sha256.Sum256(secret)
		if subtle.ConstantTimeCompare(sum[:], hash) != 1 || exp <= s.now().UnixMilli() {
			return ErrWebLogin
		}
		if !approved.Valid {
			return nil
		}
		dev, err := getDeviceTx(tx, approved.String)
		if err != nil || dev.Revoked() || dev.UserID == "" {
			return ErrWebLogin
		}
		if _, err := tx.Exec(`DELETE FROM web_logins WHERE id = ?`, id); err != nil {
			return err
		}
		token, sess, err = s.createWebSessionTx(tx, dev, ua, ip)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return token, sess, nil
}

func (s *Service) createWebSessionTx(tx *sql.Tx, dev *Device, ua, ip string) (string, *WebSession, error) {
	secret, err := ncrypto.Random(32)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(secret)
	now := s.now()
	sess := &WebSession{ID: ids.New(), UserID: dev.UserID, DeviceID: dev.ID, UserAgent: ua, IP: ip,
		CreatedAt: now.UnixMilli(), LastSeen: now.UnixMilli(), ExpiresAt: now.Add(WebSessionMaxLife).UnixMilli()}
	if _, err := tx.Exec(`
		INSERT INTO web_sessions(id, hash, user_id, device_id, user_agent, ip, created_at, last_seen, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sess.ID, sum[:], sess.UserID, sess.DeviceID, sess.UserAgent, sess.IP, sess.CreatedAt, sess.LastSeen, sess.ExpiresAt); err != nil {
		return "", nil, err
	}
	return webSessionPrefix + sess.ID + "_" + base64.RawURLEncoding.EncodeToString(secret), sess, nil
}

// AuthenticateWebSession returns the session and the device it acts as. It
// fails for expired or idle sessions, revoked devices and disabled users.
func (s *Service) AuthenticateWebSession(ctx context.Context, token string) (*WebSession, *Device, error) {
	id, secret, ok := splitSecret(token, webSessionPrefix)
	if !ok {
		return nil, nil, ErrWebSession
	}
	var sess WebSession
	var hash []byte
	err := s.Store.DB.QueryRowContext(ctx, `
		SELECT id, hash, user_id, device_id, user_agent, ip, created_at, last_seen, expires_at FROM web_sessions WHERE id = ?
	`, id).Scan(&sess.ID, &hash, &sess.UserID, &sess.DeviceID, &sess.UserAgent, &sess.IP, &sess.CreatedAt, &sess.LastSeen, &sess.ExpiresAt)
	if err != nil {
		return nil, nil, ErrWebSession
	}
	sum := sha256.Sum256(secret)
	now := s.now()
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 || sess.ExpiresAt <= now.UnixMilli() ||
		now.Sub(time.UnixMilli(sess.LastSeen)) > WebSessionIdle {
		return nil, nil, ErrWebSession
	}
	dev, err := s.Get(ctx, sess.DeviceID)
	if err != nil || dev.Revoked() || dev.UserID != sess.UserID {
		return nil, nil, ErrWebSession
	}
	u, err := s.GetUser(ctx, sess.UserID)
	if err != nil || !u.Active() {
		return nil, nil, ErrWebSession
	}
	if now.Sub(time.UnixMilli(sess.LastSeen)) > time.Minute {
		sess.LastSeen = now.UnixMilli()
		_, _ = s.Store.DB.ExecContext(ctx, `UPDATE web_sessions SET last_seen = ? WHERE id = ?`, sess.LastSeen, sess.ID)
	}
	return &sess, dev, nil
}

// ListWebSessions lists a user's live web sessions.
func (s *Service) ListWebSessions(ctx context.Context, userID string) ([]WebSession, error) {
	idle := s.now().Add(-WebSessionIdle).UnixMilli()
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT id, user_id, device_id, user_agent, ip, created_at, last_seen, expires_at FROM web_sessions
		WHERE user_id = ? AND expires_at > ? AND last_seen > ? ORDER BY last_seen DESC
	`, userID, s.now().UnixMilli(), idle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebSession{}
	for rows.Next() {
		var w WebSession
		if err := rows.Scan(&w.ID, &w.UserID, &w.DeviceID, &w.UserAgent, &w.IP, &w.CreatedAt, &w.LastSeen, &w.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// EndWebSession signs a session out. userID must own it ("" = any, for the
// session's own sign-out).
func (s *Service) EndWebSession(ctx context.Context, userID, id string) error {
	q, args := `DELETE FROM web_sessions WHERE id = ?`, []any{id}
	if userID != "" {
		q, args = q+` AND user_id = ?`, append(args, userID)
	}
	res, err := s.Store.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.Store.DB.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at < ?`, s.now().UnixMilli())
	return nil
}

func getDeviceTx(tx *sql.Tx, id string) (*Device, error) {
	return scanDeviceRow(tx.QueryRow(deviceSelect+` WHERE d.id = ?`, id))
}
