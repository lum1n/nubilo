package auth_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"nubilo/internal/auth"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/store"
)

func harness(t *testing.T) (*auth.Authenticator, *identity.Device, ed25519.PrivateKey) {
	t.Helper()
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ids := identity.NewService(st)
	pub, priv, _ := ncrypto.GenerateEd25519()
	dev, err := ids.Enroll(context.Background(), "dev", pub, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	a := &auth.Authenticator{IDs: ids, Store: st, SkewMS: 60_000}
	return a, dev, priv
}

func TestSignAndAuth(t *testing.T) {
	a, dev, priv := harness(t)
	body := []byte(`{"ok":true}`)
	ts := time.Now().UnixMilli()
	nonce := "0123456789abcdef0123456789abcdef"
	hdr := auth.SignRequest(priv, dev.ID, "POST", "/sync/v1/push", body, ts, nonce)
	r := httptest.NewRequest("POST", "/sync/v1/push", bytes.NewReader(body))
	r.Header.Set("Authorization", hdr)
	got, err := a.AuthenticateRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != dev.ID {
		t.Fatal(got.ID)
	}
}

func TestReplayNonce(t *testing.T) {
	a, dev, priv := harness(t)
	body := []byte(`{}`)
	ts := time.Now().UnixMilli()
	nonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hdr := auth.SignRequest(priv, dev.ID, "GET", "/api/v1/status", body, ts, nonce)
	mk := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/status", bytes.NewReader(body))
		r.Header.Set("Authorization", hdr)
		return r
	}
	if _, err := a.AuthenticateRequest(mk()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AuthenticateRequest(mk()); err == nil {
		t.Fatal("expected replay")
	}
}

func TestSkew(t *testing.T) {
	a, dev, priv := harness(t)
	body := []byte{}
	ts := time.Now().UnixMilli() - 10*60*1000
	hdr := auth.SignRequest(priv, dev.ID, "GET", "/x", body, ts, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	r := httptest.NewRequest("GET", "/x", bytes.NewReader(body))
	r.Header.Set("Authorization", hdr)
	if _, err := a.AuthenticateRequest(r); err == nil {
		t.Fatal("expected skew")
	}
}

func TestRevokedDeviceAuth(t *testing.T) {
	a, dev, priv := harness(t)
	if err := a.IDs.Revoke(context.Background(), dev.ID); err != nil {
		t.Fatal(err)
	}
	body := []byte{}
	hdr := auth.SignRequest(priv, dev.ID, "GET", "/x", body, time.Now().UnixMilli(), "cccccccccccccccccccccccccccccccc")
	r := httptest.NewRequest("GET", "/x", bytes.NewReader(body))
	r.Header.Set("Authorization", hdr)
	if _, err := a.AuthenticateRequest(r); err == nil {
		t.Fatal("expected revoked")
	}
}

func TestBadSignature(t *testing.T) {
	a, dev, _ := harness(t)
	_, priv2, _ := ncrypto.GenerateEd25519()
	body := []byte("x")
	hdr := auth.SignRequest(priv2, dev.ID, "POST", "/x", body, time.Now().UnixMilli(), "dddddddddddddddddddddddddddddddd")
	r := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
	r.Header.Set("Authorization", hdr)
	if _, err := a.AuthenticateRequest(r); err == nil {
		t.Fatal("expected bad sig")
	}
}

func TestFuzzParseDoesNotPanic(t *testing.T) {
	cases := []string{"", "Bearer x", "Nubilo-Sig v1", "Nubilo-Sig v1 device=,ts=x,nonce=,sig=", "Nubilo-Sig v2 device=a,ts=1,nonce=aa,sig=YQ"}
	for _, c := range cases {
		_, _ = auth.ParseAuthorization(c)
	}
}

func TestAdminTokenLoopbackOnly(t *testing.T) {
	a, _, _ := harness(t)
	a.AdminTok = []byte("secret-admin-token")
	mk := func(remote string) *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/status", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Nubilo-Admin", "secret-admin-token")
		return r
	}
	if _, err := a.AuthenticateRequest(mk("127.0.0.1:9")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AuthenticateRequest(mk("8.8.8.8:9")); err == nil {
		t.Fatal("non-loopback admin token")
	}
	r := httptest.NewRequest("GET", "/api/v1/status", nil)
	r.RemoteAddr = "127.0.0.1:9"
	r.Header.Set("X-Nubilo-Admin", "wrong")
	if _, err := a.AuthenticateRequest(r); err == nil {
		t.Fatal("wrong token")
	}
}

func TestBodyTooLarge(t *testing.T) {
	a, dev, priv := harness(t)
	a.MaxBody = 4
	body := []byte("12345")
	hdr := auth.SignRequest(priv, dev.ID, "POST", "/x", body, time.Now().UnixMilli(), "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	r := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
	r.Header.Set("Authorization", hdr)
	if _, err := a.AuthenticateRequest(r); err == nil {
		t.Fatal("expected too large")
	}
}

func FuzzCanonicalStable(f *testing.F) {
	f.Add("dev", int64(1), "nonce", "POST", "/x", []byte("body"))
	f.Fuzz(func(t *testing.T, device string, ts int64, nonce, method, path string, body []byte) {
		a := auth.Canonical(device, ts, nonce, method, path, body)
		b := auth.Canonical(device, ts, nonce, method, path, body)
		if string(a) != string(b) {
			t.Fatal("canonical not stable")
		}
	})
}

func FuzzParseAuthorization(f *testing.F) {
	f.Add("Nubilo-Sig v1 device=01,ts=1,nonce=0123456789abcdef,sig=YQ")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = auth.ParseAuthorization(s)
	})
}
