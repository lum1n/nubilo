package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nubilo/internal/auth"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

type signer struct {
	t    *testing.T
	ts   *httptest.Server
	dev  *identity.Device
	priv ed25519.PrivateKey
}

func (c *signer) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		raw, _ = json.Marshal(b)
	}
	req, err := http.NewRequest(method, c.ts.URL+path, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", auth.SignRequest(c.priv, c.dev.ID, method, path, raw, time.Now().UnixMilli(), nonce()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *signer) json(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	code, out := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
	m := map[string]any{}
	_ = json.Unmarshal(out, &m)
	return m
}

type familyServer struct {
	ts                *httptest.Server
	ids               *identity.Service
	st                *store.Store
	eng               *syncengine.Engine
	admin, alice, bob *signer
}

func startFamily(t *testing.T) *familyServer {
	t.Helper()
	ts, idsvc, st := start(t)
	eng := syncengine.New(st)
	idsvc.OnUserCreated = func(ctx context.Context, u *identity.User) error { return eng.EnsureUserDefaults(ctx, u.ID) }
	f := &familyServer{ts: ts, ids: idsvc, st: st, eng: eng}
	f.admin = f.user(t, "mum", identity.UserAdmin)
	f.alice = f.user(t, "alice", identity.UserMember)
	f.bob = f.user(t, "bob", identity.UserMember)
	return f
}

func (f *familyServer) user(t *testing.T, name string, role identity.UserRole) *signer {
	t.Helper()
	u, err := f.ids.CreateUser(context.Background(), name, "", role)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ncrypto.GenerateEd25519()
	dev, err := f.ids.Enroll(context.Background(), u.ID, name+"-phone", pub, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{t: t, ts: f.ts, dev: dev, priv: priv}
}

func (f *familyServer) files(t *testing.T, s *signer) string {
	t.Helper()
	c, err := f.eng.FindChildCollection(context.Background(), s.dev.UserID, "files", "", "Files")
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func putBlob(s *signer, payload []byte) (int, string) {
	sum := ncrypto.SHA256Hex(payload)
	code, _ := s.do(http.MethodPut, "/sync/v1/blob/"+sum, payload)
	return code, sum
}

func pushCreate(s *signer, colID, hash string, size int) map[string]any {
	return s.json(http.MethodPost, "/sync/v1/push", map[string]any{
		"idempotency_key": nonce(),
		"changes": []map[string]any{{
			"object_id": newULID(), "collection_id": colID, "op": "create",
			"content_hash": hash, "blob_id": hash, "size": size, "metadata": map[string]string{"name": "x.txt"},
		}},
	}, http.StatusOK)
}

func pushStatus(m map[string]any) string {
	res, _ := m["results"].([]any)
	if len(res) == 0 {
		return ""
	}
	r, _ := res[0].(map[string]any)
	s, _ := r["status"].(string)
	if e, _ := r["error"].(string); e != "" {
		s += ":" + e
	}
	return s
}

func TestUserIsolation(t *testing.T) {
	f := startFamily(t)
	aliceFiles := f.files(t, f.alice)

	code, secret := putBlob(f.alice, []byte("alice's diary"))
	if code != http.StatusOK {
		t.Fatalf("alice blob put %d", code)
	}
	if got := pushStatus(pushCreate(f.alice, aliceFiles, secret, 13)); got != "ok" {
		t.Fatalf("alice push %s", got)
	}

	// Bob cannot read Alice's blob, even knowing the hash.
	if code, _ := f.bob.do(http.MethodGet, "/sync/v1/blob/"+secret, nil); code != http.StatusNotFound {
		t.Fatalf("bob read alice blob: %d", code)
	}
	if code, _ := f.alice.do(http.MethodGet, "/sync/v1/blob/"+secret, nil); code != http.StatusOK {
		t.Fatalf("alice read own blob: %d", code)
	}
	// Bob cannot push into Alice's collection.
	_, bobBlob := putBlob(f.bob, []byte("bob was here"))
	if got := pushStatus(pushCreate(f.bob, aliceFiles, bobBlob, 12)); got != "error:unauthorized" {
		t.Fatalf("bob push into alice: %s", got)
	}
	// Bob's collection list and changes contain nothing of Alice's.
	cols := f.bob.json(http.MethodPost, "/sync/v1/collections", map[string]any{}, http.StatusOK)
	for _, c := range cols["collections"].([]any) {
		if c.(map[string]any)["id"] == aliceFiles {
			t.Fatal("alice's collection visible to bob")
		}
	}
	ch := f.bob.json(http.MethodPost, "/sync/v1/changes", map[string]any{"since_seq": 0, "limit": 100}, http.StatusOK)
	if n := len(ch["changes"].([]any)); n != 0 {
		t.Fatalf("bob sees %d changes", n)
	}
	if code, _ := f.bob.do(http.MethodPost, "/sync/v1/changes", map[string]any{"since_seq": 0, "collection_id": aliceFiles}); code != http.StatusForbidden {
		t.Fatalf("bob changes for alice collection: %d", code)
	}
	// Devices: members see only their own.
	code, out := f.bob.do(http.MethodGet, "/api/v1/devices", nil)
	if code != http.StatusOK || strings.Contains(string(out), f.alice.dev.ID) {
		t.Fatalf("bob device list %d %s", code, out)
	}
	if code, _ := f.bob.do(http.MethodPost, "/api/v1/devices/"+f.alice.dev.ID+"/revoke", nil); code != http.StatusForbidden {
		t.Fatalf("bob revoked alice's device: %d", code)
	}
	if code, _ := f.bob.do(http.MethodGet, "/api/v1/devices?all=1", nil); code != http.StatusOK {
		t.Fatal("devices?all=1 should fall back to own devices for members")
	}

	// Read-only share: Bob reads but cannot write.
	f.alice.json(http.MethodPut, "/api/v1/collections/"+aliceFiles+"/shares", map[string]any{"username": "bob", "access": "ro"}, http.StatusOK)
	if code, _ := f.bob.do(http.MethodGet, "/sync/v1/blob/"+secret, nil); code != http.StatusOK {
		t.Fatalf("ro share blob read %d", code)
	}
	if got := pushStatus(pushCreate(f.bob, aliceFiles, bobBlob, 12)); got != "error:unauthorized" {
		t.Fatalf("ro share push: %s", got)
	}
	ch = f.bob.json(http.MethodPost, "/sync/v1/changes", map[string]any{"since_seq": 0, "limit": 100}, http.StatusOK)
	if n := len(ch["changes"].([]any)); n != 1 {
		t.Fatalf("ro share changes %d", n)
	}
	// Read-write share: Bob can add files.
	f.alice.json(http.MethodPut, "/api/v1/collections/"+aliceFiles+"/shares", map[string]any{"username": "bob", "access": "rw"}, http.StatusOK)
	if got := pushStatus(pushCreate(f.bob, aliceFiles, bobBlob, 12)); got != "ok" {
		t.Fatalf("rw share push: %s", got)
	}
	// Only the owner manages shares.
	if code, _ := f.bob.do(http.MethodGet, "/api/v1/collections/"+aliceFiles+"/shares", nil); code != http.StatusForbidden {
		t.Fatalf("bob listed shares: %d", code)
	}
	list := f.bob.json(http.MethodGet, "/api/v1/collections", nil, http.StatusOK)
	var sawShared bool
	for _, c := range list["collections"].([]any) {
		m := c.(map[string]any)
		if m["id"] == aliceFiles {
			sawShared = m["access"] == "rw" && m["owner"] == "alice" && m["shares"] == nil
		}
	}
	if !sawShared {
		t.Fatalf("shared collection listing %v", list)
	}
}

func TestPhotosIsolation(t *testing.T) {
	f := startFamily(t)
	img := jpegBody(t)
	code, out := f.alice.do(http.MethodPost, "/api/v1/photos?name=a.jpg", img)
	if code != http.StatusCreated {
		t.Fatalf("upload %d %s", code, out)
	}
	var meta map[string]any
	_ = json.Unmarshal(out, &meta)
	id := meta["id"].(string)
	bobList := f.bob.json(http.MethodGet, "/api/v1/photos", nil, http.StatusOK)
	if n := len(bobList["photos"].([]any)); n != 0 {
		t.Fatalf("bob sees %d photos", n)
	}
	for _, r := range []string{"", "/original", "/thumb"} {
		if code, _ := f.bob.do(http.MethodGet, "/api/v1/photos/"+id+r, nil); code != http.StatusNotFound {
			t.Fatalf("bob photo%s %d", r, code)
		}
	}
	// Shared album: Bob sees the item but not Alice's library.
	album := f.alice.json(http.MethodPost, "/api/v1/albums", map[string]string{"name": "Holiday"}, http.StatusCreated)
	albumID := album["id"].(string)
	f.alice.json(http.MethodPost, "/api/v1/albums/"+albumID+"/items", map[string]any{"photo_ids": []string{id}}, http.StatusOK)
	f.alice.json(http.MethodPut, "/api/v1/collections/"+albumID+"/shares", map[string]any{"username": "bob", "access": "ro"}, http.StatusOK)
	albums := f.bob.json(http.MethodGet, "/api/v1/albums", nil, http.StatusOK)
	if !strings.Contains(albumsJSON(albums), "Holiday") {
		t.Fatalf("bob albums %v", albums)
	}
	items := f.bob.json(http.MethodGet, "/api/v1/albums/"+albumID+"/items", nil, http.StatusOK)
	list := items["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("album items %v", items)
	}
	itemID := list[0].(map[string]any)["id"].(string)
	if code, _ := f.bob.do(http.MethodGet, "/api/v1/photos/"+itemID+"/thumb", nil); code != http.StatusOK {
		t.Fatalf("bob album thumb %d", code)
	}
	if code, _ := f.bob.do(http.MethodPost, "/api/v1/albums/"+albumID+"/items", map[string]any{"photo_ids": []string{itemID}}); code != http.StatusForbidden {
		t.Fatalf("bob wrote to ro album: %d", code)
	}
}

func albumsJSON(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestMemberCannotAdmin(t *testing.T) {
	f := startFamily(t)
	for _, p := range []string{"/api/v1/admin/users", "/api/v1/admin/invites", "/api/v1/admin/stats"} {
		if code, _ := f.bob.do(http.MethodGet, p, nil); code != http.StatusForbidden {
			t.Fatalf("member GET %s: %d", p, code)
		}
	}
	if code, _ := f.bob.do(http.MethodPost, "/api/v1/admin/invites", map[string]any{"role": "admin"}); code != http.StatusForbidden {
		t.Fatalf("member created invite: %d", code)
	}
	stats := f.admin.json(http.MethodGet, "/api/v1/admin/stats", nil, http.StatusOK)
	if n := len(stats["users"].([]any)); n != 3 {
		t.Fatalf("admin stats users %d", n)
	}
	// Admins can revoke any device; disabling a user revokes theirs.
	f.admin.json(http.MethodPatch, "/api/v1/admin/users/"+f.bob.dev.UserID, map[string]any{"status": "disabled"}, http.StatusOK)
	if code, _ := f.bob.do(http.MethodGet, "/api/v1/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("disabled user still authenticated: %d", code)
	}
	if code, _ := f.admin.do(http.MethodPatch, "/api/v1/admin/users/"+f.admin.dev.UserID, map[string]any{"role": "member"}); code != http.StatusConflict {
		t.Fatalf("demoting the last admin: %d", code)
	}
}

func TestInviteRedeemAndMe(t *testing.T) {
	f := startFamily(t)
	inv := f.admin.json(http.MethodPost, "/api/v1/admin/invites", map[string]any{"role": "member"}, http.StatusCreated)
	code := inv["code"].(string)
	if !strings.HasPrefix(code, "nbi-") || !strings.Contains(inv["url"].(string), "/join#"+code) {
		t.Fatalf("invite %v", inv)
	}
	pub, priv, _ := ncrypto.GenerateEd25519()
	redeem := func(body map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		resp, err := http.Post(f.ts.URL+"/api/v1/invites/redeem", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		m := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	st, _ := redeem(map[string]any{"code": code, "username": "alice", "device_name": "phone", "public_key": base64.RawStdEncoding.EncodeToString(pub)})
	if st != http.StatusConflict {
		t.Fatalf("taken username: %d", st)
	}
	st, rr := redeem(map[string]any{"code": code, "username": "carol", "display_name": "Carol", "device_name": "phone", "public_key": base64.RawStdEncoding.EncodeToString(pub)})
	if st != http.StatusOK {
		t.Fatalf("redeem %d %v", st, rr)
	}
	chal, _ := base64.RawStdEncoding.DecodeString(rr["challenge"].(string))
	comp, _ := json.Marshal(map[string]string{"pairing_id": rr["pairing_id"].(string), "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, chal))})
	resp, err := http.Post(f.ts.URL+"/api/v1/pair/complete", "application/json", bytes.NewReader(comp))
	if err != nil {
		t.Fatal(err)
	}
	var cr struct {
		DeviceID string `json:"device_id"`
		UserID   string `json:"user_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || cr.UserID == "" {
		t.Fatalf("complete %d %+v", resp.StatusCode, cr)
	}
	dev, err := f.ids.Get(context.Background(), cr.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	carol := &signer{t: t, ts: f.ts, dev: dev, priv: priv}
	me := carol.json(http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	if me["user"].(map[string]any)["username"] != "carol" {
		t.Fatalf("me %v", me)
	}
	if !strings.HasSuffix(me["urls"].(map[string]any)["caldav"].(string), "/caldav/u/carol/") {
		t.Fatalf("me urls %v", me["urls"])
	}
	// Single use.
	pub2, _, _ := ncrypto.GenerateEd25519()
	if st, _ := redeem(map[string]any{"code": code, "username": "dave", "device_name": "p", "public_key": base64.RawStdEncoding.EncodeToString(pub2)}); st != http.StatusUnauthorized {
		t.Fatalf("second redeem %d", st)
	}

	// App passwords and the single-use profile.
	pw := carol.json(http.MethodPost, "/api/v1/me/passwords", map[string]string{"label": "Laptop", "scope": "webdav"}, http.StatusCreated)
	if pw["username"] != "carol" || pw["password"] == "" {
		t.Fatalf("password %v", pw)
	}
	prof := carol.json(http.MethodPost, "/api/v1/me/profile", map[string]any{"services": []string{"caldav", "carddav"}}, http.StatusCreated)
	purl := prof["url"].(string)
	path := purl[strings.Index(purl, "/api/v1/profile/"):]
	resp, err = http.Get(f.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-apple-aspen-config" ||
		!strings.Contains(string(body), "com.apple.caldav.account") || !strings.Contains(string(body), "/caldav/u/carol/") {
		t.Fatalf("profile %d %s", resp.StatusCode, body)
	}
	resp, _ = http.Get(f.ts.URL + path)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("profile reused: %d", resp.StatusCode)
	}
	list := carol.json(http.MethodGet, "/api/v1/me/passwords", nil, http.StatusOK)
	if n := len(list["passwords"].([]any)); n != 2 {
		t.Fatalf("passwords %d", n)
	}
}

func TestUploadTokenFlow(t *testing.T) {
	f := startFamily(t)
	tok := f.alice.json(http.MethodPost, "/api/v1/me/upload-token", nil, http.StatusOK)["token"].(string)
	img := jpegBody(t)
	sum := ncrypto.SHA256Hex(img)
	bearer := func(method, path string, body []byte) (int, []byte) {
		req, _ := http.NewRequest(method, f.ts.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	raw, _ := json.Marshal(map[string]any{"hashes": []string{sum}})
	code, out := bearer(http.MethodPost, "/api/v1/photos/check", raw)
	if code != http.StatusOK || !strings.Contains(string(out), sum) {
		t.Fatalf("check %d %s", code, out)
	}
	if code, out := bearer(http.MethodPut, "/sync/v1/blob/"+sum, img); code != http.StatusOK {
		t.Fatalf("blob put %d %s", code, out)
	}
	raw, _ = json.Marshal(map[string]any{"blob_id": sum, "name": "IMG_1.jpg", "local_id": "ABC/L0/001"})
	if code, out := bearer(http.MethodPost, "/api/v1/photos/commit", raw); code != http.StatusCreated {
		t.Fatalf("commit %d %s", code, out)
	}
	raw, _ = json.Marshal(map[string]any{"hashes": []string{sum}})
	if _, out := bearer(http.MethodPost, "/api/v1/photos/check", raw); strings.Contains(string(out), sum) {
		t.Fatalf("still missing after commit: %s", out)
	}
	// The token works for nothing else.
	if code, _ := bearer(http.MethodGet, "/api/v1/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("upload token used for /me: %d", code)
	}
	// Bob cannot commit Alice's blob.
	raw, _ = json.Marshal(map[string]any{"blob_id": sum, "name": "stolen.jpg"})
	if code, _ := f.bob.do(http.MethodPost, "/api/v1/photos/commit", raw); code != http.StatusForbidden {
		t.Fatalf("bob committed alice's blob: %d", code)
	}
}

func newULID() string { return ids.New() }

func TestDeviceInviteAddsToOwnAccount(t *testing.T) {
	f := startFamily(t)
	inv := f.bob.json(http.MethodPost, "/api/v1/me/device-invite", nil, http.StatusCreated)
	code := inv["code"].(string)
	if !strings.HasPrefix(inv["setup_url"].(string), "nubilo://setup?") || inv["role"] != "member" {
		t.Fatalf("invite %v", inv)
	}
	pub, priv, _ := ncrypto.GenerateEd25519()
	redeem := func(pub ed25519.PublicKey) (int, map[string]any) {
		raw, _ := json.Marshal(map[string]any{"code": code, "username": "ignored", "device_name": "bob-mac", "public_key": base64.RawStdEncoding.EncodeToString(pub)})
		resp, err := http.Post(f.ts.URL+"/api/v1/invites/redeem", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		m := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	st, rr := redeem(pub)
	if st != http.StatusOK {
		t.Fatalf("redeem %d %v", st, rr)
	}
	chal, _ := base64.RawStdEncoding.DecodeString(rr["challenge"].(string))
	comp, _ := json.Marshal(map[string]string{"pairing_id": rr["pairing_id"].(string), "signature": base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, chal))})
	resp, err := http.Post(f.ts.URL+"/api/v1/pair/complete", "application/json", bytes.NewReader(comp))
	if err != nil {
		t.Fatal(err)
	}
	var cr struct {
		DeviceID string `json:"device_id"`
		UserID   string `json:"user_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || cr.UserID != f.bob.dev.UserID {
		t.Fatalf("complete %d %+v, want user %s", resp.StatusCode, cr, f.bob.dev.UserID)
	}
	if _, err := f.ids.GetUserByName(context.Background(), "ignored"); err == nil {
		t.Fatal("device invite created a new account")
	}
	pub2, _, _ := ncrypto.GenerateEd25519()
	if st, _ := redeem(pub2); st != http.StatusUnauthorized {
		t.Fatalf("second redeem %d", st)
	}

	// An app password (dav device) cannot mint one.
	dpub, dpriv, _ := ncrypto.GenerateEd25519()
	ddev, err := f.ids.Enroll(context.Background(), f.bob.dev.UserID, "bob-dav", dpub, identity.RoleDAV)
	if err != nil {
		t.Fatal(err)
	}
	dav := &signer{t: t, ts: f.ts, dev: ddev, priv: dpriv}
	if code, _ := dav.do(http.MethodPost, "/api/v1/me/device-invite", nil); code != http.StatusForbidden {
		t.Fatalf("dav device invite: %d", code)
	}
}
