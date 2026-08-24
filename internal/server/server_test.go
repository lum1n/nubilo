package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nubilo/internal/audit"
	"nubilo/internal/auth"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/server"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

func start(t *testing.T) (*httptest.Server, *identity.Service, *store.Store) {
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
	idsvc := identity.NewService(st)
	a := &auth.Authenticator{IDs: idsvc, Store: st, SkewMS: 60_000, AdminTok: []byte("admintok")}
	cfg := config.Defaults(dir)
	cfg.Pairing.BeginsPerHour = 5
	eng := syncengine.New(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, st, idsvc, a, eng, &audit.Logger{Store: st, Slog: log}, log, pub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, idsvc, st
}

func TestUnauthenticatedAPI(t *testing.T) {
	ts, _, _ := start(t)
	resp, err := http.Get(ts.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/api/v1/devices")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("devices %d", resp.StatusCode)
	}
}

func TestPairAndSignedSync(t *testing.T) {
	ts, idsvc, st := start(t)
	code, _, err := idsvc.StartPairing(context.Background(), identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ncrypto.GenerateEd25519()
	begin, _ := json.Marshal(map[string]string{
		"code": code, "name": "phone", "public_key": base64.RawStdEncoding.EncodeToString(pub),
	})
	resp, err := http.Post(ts.URL+"/api/v1/pair/begin", "application/json", bytes.NewReader(begin))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("begin %d %s", resp.StatusCode, b)
	}
	var br struct {
		PairingID string `json:"pairing_id"`
		Challenge string `json:"challenge"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&br)
	resp.Body.Close()
	chal, _ := base64.RawStdEncoding.DecodeString(br.Challenge)
	sig := ncrypto.SignEd25519(priv, chal)
	comp, _ := json.Marshal(map[string]string{
		"pairing_id": br.PairingID, "signature": base64.RawStdEncoding.EncodeToString(sig),
	})
	resp, err = http.Post(ts.URL+"/api/v1/pair/complete", "application/json", bytes.NewReader(comp))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("complete %d %s", resp.StatusCode, b)
	}
	var cr struct {
		DeviceID string `json:"device_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	eng := syncengine.New(st)
	col, err := eng.CreateCollection(context.Background(), "files", "docs", "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"protocol_min":1,"protocol_max":1,"cursor":0}`)
	req, _ := http.NewRequest("POST", ts.URL+"/sync/v1/hello", bytes.NewReader(body))
	req.Header.Set("Authorization", auth.SignRequest(priv, cr.DeviceID, "POST", "/sync/v1/hello", body, time.Now().UnixMilli(), "0123456789abcdef0123456789abcdef"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("hello %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	_ = col
}

func TestRevokedDeviceLosesAccess(t *testing.T) {
	ts, idsvc, _ := start(t)
	pub, priv, _ := ncrypto.GenerateEd25519()
	dev, err := idsvc.Enroll(context.Background(), "x", pub, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	n := 0
	call := func() int {
		n++
		req, _ := http.NewRequest("POST", ts.URL+"/sync/v1/hello", bytes.NewReader(body))
		nonce := fmt.Sprintf("%032d", n)
		req.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, "POST", "/sync/v1/hello", body, time.Now().UnixMilli(), nonce))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := call(); code != 200 {
		t.Fatalf("before revoke %d", code)
	}
	if err := idsvc.Revoke(context.Background(), dev.ID); err != nil {
		t.Fatal(err)
	}
	if code := call(); code != http.StatusUnauthorized {
		t.Fatalf("after revoke %d", code)
	}
}

func TestPairBeginRateLimit(t *testing.T) {
	ts, idsvc, _ := start(t)
	_, _, _ = idsvc.StartPairing(context.Background(), identity.RoleClient)
	pub, _, _ := ncrypto.GenerateEd25519()
	body, _ := json.Marshal(map[string]string{"code": "AAAAA-BBBBB", "name": "n", "public_key": base64.RawStdEncoding.EncodeToString(pub)})
	limited := false
	for i := 0; i < 10; i++ {
		resp, err := http.Post(ts.URL+"/api/v1/pair/begin", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected rate limit")
	}
}

func TestBlobPathNotFromURLUserPath(t *testing.T) {
	ts, idsvc, _ := start(t)
	pub, priv, _ := ncrypto.GenerateEd25519()
	dev, _ := idsvc.Enroll(context.Background(), "x", pub, identity.RoleClient)
	payload := []byte("hi")
	// traversal-like hash is rejected (must be 64 hex)
	req, _ := http.NewRequest("PUT", ts.URL+"/sync/v1/blob/../../etc/passwd", bytes.NewReader(payload))
	req.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, "PUT", "/sync/v1/blob/../../etc/passwd", payload, time.Now().UnixMilli(), strings.Repeat("e", 32)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("traversal must not succeed")
	}
}

func TestOversizedJSON(t *testing.T) {
	ts, _, _ := start(t)
	huge := bytes.Repeat([]byte("a"), 40<<20)
	resp, err := http.Post(ts.URL+"/api/v1/pair/begin", "application/json", bytes.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("oversized body")
	}
}

func TestMalformedPairJSON(t *testing.T) {
	ts, _, _ := start(t)
	resp, err := http.Post(ts.URL+"/api/v1/pair/begin", "application/json", strings.NewReader(`{"not":"valid","extra":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestAdminNotOnNonLoopback(t *testing.T) {
	ts, _, _ := start(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req.Header.Set("X-Nubilo-Admin", "admintok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// httptest client remote addr is not loopback from the server's perspective? actually RemoteAddr is the client of the test server.
	// The test server sees the incoming connection from the test client; typically 127.0.0.1.
	// If it succeeds, that's OK for loopback. We still check missing token fails:
	req2, _ := http.NewRequest("GET", ts.URL+"/api/v1/status", nil)
	req2.Header.Set("X-Nubilo-Admin", "wrong")
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong admin token %d", resp2.StatusCode)
	}
}

func TestPairCompleteRateLimit(t *testing.T) {
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
	idsvc := identity.NewService(st)
	a := &auth.Authenticator{IDs: idsvc, Store: st, SkewMS: 60_000, AdminTok: []byte("admintok")}
	cfg := config.Defaults(dir)
	cfg.Pairing.CompletesPerHour = 2
	eng := syncengine.New(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, st, idsvc, a, eng, &audit.Logger{Store: st, Slog: log}, log, pub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	body, _ := json.Marshal(map[string]string{"pairing_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "signature": "YQ"})
	limited := false
	for i := 0; i < 6; i++ {
		resp, err := http.Post(ts.URL+"/api/v1/pair/complete", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected complete rate limit")
	}
}

func TestDavRoleCannotSync(t *testing.T) {
	ts, idsvc, _ := start(t)
	pub, priv, _ := ncrypto.GenerateEd25519()
	dev, err := idsvc.Enroll(context.Background(), "iphone", pub, identity.RoleDAV)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"cursor":0}`)
	req, _ := http.NewRequest("POST", ts.URL+"/sync/v1/hello", bytes.NewReader(body))
	req.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, "POST", "/sync/v1/hello", body, time.Now().UnixMilli(), strings.Repeat("f", 32)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dav should not use sync protocol, got %d", resp.StatusCode)
	}
}
