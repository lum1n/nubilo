package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nubilo/internal/identity"
	"nubilo/internal/store"
)

var (
	ErrMissingAuth  = errors.New("auth: missing authorization")
	ErrMalformed    = errors.New("auth: malformed authorization")
	ErrReplay       = errors.New("auth: replayed nonce")
	ErrSkew         = errors.New("auth: timestamp outside allowed window")
	ErrSignature    = errors.New("auth: bad signature")
	ErrRevoked      = errors.New("auth: device revoked")
	ErrNoKey        = errors.New("auth: device has no public key")
	ErrAdminToken   = errors.New("auth: invalid admin token")
	ErrBodyTooLarge = errors.New("auth: request body too large")
	ErrBodyRead     = errors.New("auth: incomplete request body")
)

const scheme = "Nubilo-Sig"

type Header struct {
	DeviceID string
	TS       int64
	Nonce    string
	Sig      []byte
}

func ParseAuthorization(v string) (Header, error) {
	var h Header
	v = strings.TrimSpace(v)
	if v == "" {
		return h, ErrMissingAuth
	}
	if !strings.HasPrefix(v, scheme) {
		return h, ErrMalformed
	}
	rest := strings.TrimSpace(strings.TrimPrefix(v, scheme))
	if !strings.HasPrefix(rest, "v1") {
		return h, ErrMalformed
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "v1"))
	parts := strings.Split(rest, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			return h, ErrMalformed
		}
		k = strings.TrimSpace(k)
		val = strings.TrimSpace(val)
		switch k {
		case "device":
			h.DeviceID = val
		case "ts":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return h, ErrMalformed
			}
			h.TS = n
		case "nonce":
			h.Nonce = val
		case "sig":
			b, err := base64.RawStdEncoding.DecodeString(val)
			if err != nil {
				b, err = base64.StdEncoding.DecodeString(val)
				if err != nil {
					return h, ErrMalformed
				}
			}
			h.Sig = b
		default:
			return h, ErrMalformed
		}
	}
	if h.DeviceID == "" || h.TS == 0 || h.Nonce == "" || len(h.Sig) == 0 {
		return h, ErrMalformed
	}
	if len(h.Nonce) < 16 || len(h.Nonce) > 128 {
		return h, ErrMalformed
	}
	return h, nil
}

func Canonical(deviceID string, ts int64, nonce, method, path string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(fmt.Sprintf("nubilo-sig/v1\n%s\n%d\n%s\n%s\n%s\n%s",
		deviceID, ts, nonce, strings.ToUpper(method), path, hex.EncodeToString(sum[:])))
}

func FormatAuthorization(h Header) string {
	return fmt.Sprintf("%s v1 device=%s,ts=%d,nonce=%s,sig=%s",
		scheme, h.DeviceID, h.TS, h.Nonce, base64.RawStdEncoding.EncodeToString(h.Sig))
}

type Authenticator struct {
	IDs      *identity.Service
	Store    *store.Store
	SkewMS   int64
	AdminTok []byte
	MaxBody  int64
	Now      func() time.Time
}

func (a *Authenticator) AuthenticateRequest(r *http.Request) (*identity.Device, error) {
	if tok := r.Header.Get("X-Nubilo-Admin"); tok != "" {
		return a.adminDevice(r, tok)
	}
	h, err := ParseAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	now := a.now().UnixMilli()
	if a.SkewMS <= 0 {
		a.SkewMS = 60_000
	}
	delta := now - h.TS
	if delta < 0 {
		delta = -delta
	}
	if delta > a.SkewMS {
		return nil, ErrSkew
	}
	dev, err := a.IDs.Get(r.Context(), h.DeviceID)
	if err != nil {
		return nil, err
	}
	if dev.Revoked() {
		return nil, ErrRevoked
	}
	if len(dev.PublicKey) != ed25519.PublicKeySize {
		return nil, ErrNoKey
	}
	body, err := a.readBody(r)
	if err != nil {
		return nil, err
	}
	path := r.URL.RequestURI()
	msg := Canonical(h.DeviceID, h.TS, h.Nonce, r.Method, path, body)
	if !ed25519.Verify(ed25519.PublicKey(dev.PublicKey), msg, h.Sig) {
		return nil, ErrSignature
	}
	if err := a.consumeNonce(r.Context(), h.DeviceID, h.Nonce, h.TS); err != nil {
		return nil, err
	}
	a.IDs.Touch(r.Context(), dev.ID)
	return dev, nil
}

func (a *Authenticator) adminDevice(r *http.Request, tok string) (*identity.Device, error) {
	if len(a.AdminTok) == 0 {
		return nil, ErrAdminToken
	}
	host := r.RemoteAddr
	if !isLoopbackAddr(host) {
		return nil, ErrAdminToken
	}
	sumTok := sha256.Sum256([]byte(tok))
	sumWant := sha256.Sum256(a.AdminTok)
	if subtle.ConstantTimeCompare(sumTok[:], sumWant[:]) != 1 {
		return nil, ErrAdminToken
	}
	return &identity.Device{
		ID:          "admin-local",
		Name:        "local-admin",
		Role:        identity.RoleAdmin,
		Permissions: identity.DefaultPermissions(identity.RoleAdmin),
	}, nil
}

func (a *Authenticator) consumeNonce(ctx context.Context, deviceID, nonce string, ts int64) error {
	_, err := a.Store.DB.ExecContext(ctx, `INSERT INTO nonces(device_id, nonce, ts) VALUES (?, ?, ?)`, deviceID, nonce, ts)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrReplay
		}
		return err
	}
	cutoff := a.now().UnixMilli() - 2*a.SkewMS
	_, _ = a.Store.DB.ExecContext(ctx, `DELETE FROM nonces WHERE ts < ?`, cutoff)
	return nil
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authenticator) readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return []byte{}, nil
	}
	max := a.MaxBody
	if max <= 0 {
		max = 65 << 20
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBodyRead, err)
	}
	if int64(len(b)) > max {
		return nil, ErrBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// CountsTowardFailLimit reports whether a failed auth should burn the per-IP
// attempt budget (signature abuse). Size / transport errors should not.
func CountsTowardFailLimit(err error) bool {
	if err == nil ||
		errors.Is(err, ErrMissingAuth) ||
		errors.Is(err, ErrBodyTooLarge) ||
		errors.Is(err, ErrBodyRead) {
		return false
	}
	return errors.Is(err, ErrSignature) ||
		errors.Is(err, ErrMalformed) ||
		errors.Is(err, ErrReplay) ||
		errors.Is(err, ErrSkew) ||
		errors.Is(err, ErrRevoked) ||
		errors.Is(err, ErrNoKey) ||
		errors.Is(err, ErrAdminToken)
}

func isLoopbackAddr(remote string) bool {
	host := remote
	if i := strings.LastIndex(remote, ":"); i >= 0 {
		host = remote[:i]
	}
	host = strings.Trim(host, "[]")
	if host == "127.0.0.1" || host == "::1" || host == "localhost" || host == "::ffff:127.0.0.1" {
		return true
	}
	return false
}

func SignRequest(priv ed25519.PrivateKey, deviceID, method, path string, body []byte, ts int64, nonce string) string {
	msg := Canonical(deviceID, ts, nonce, method, path, body)
	sig := ed25519.Sign(priv, msg)
	return FormatAuthorization(Header{DeviceID: deviceID, TS: ts, Nonce: nonce, Sig: sig})
}
