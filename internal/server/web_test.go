package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
)

// browser keeps cookies by hand: the server sets Secure cookies, which a
// cookie jar would not send to the plain-HTTP test server.
type browser struct {
	t       *testing.T
	base    string
	cookies map[string]string
}

func (b *browser) do(method, path, body string, hdr map[string]string) (int, []byte) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for k, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				b.t.Fatalf("cookie %s not Secure/HttpOnly/Strict", c.Name)
			}
			b.cookies[c.Name] = c.Value
		}
	}
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

var webHdr = map[string]string{"X-Nubilo-Web": "1"}

func (b *browser) start() string {
	b.t.Helper()
	code, out := b.do(http.MethodPost, "/web/api/login", "", webHdr)
	if code != http.StatusCreated {
		b.t.Fatalf("start %d %s", code, out)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if !strings.HasPrefix(m["qr_png"].(string), "data:image/png;base64,") || !strings.Contains(m["link"].(string), "nubilo://web-login?") {
		b.t.Fatalf("start %v", m)
	}
	return m["code"].(string)
}

func (b *browser) poll() string {
	b.t.Helper()
	_, out := b.do(http.MethodGet, "/web/api/login", "", nil)
	var m map[string]string
	_ = json.Unmarshal(out, &m)
	return m["status"]
}

func TestWebLogin(t *testing.T) {
	f := startFamily(t)
	b := &browser{t: t, base: f.ts.URL, cookies: map[string]string{}}

	if code, _ := b.do(http.MethodGet, "/web/api/me", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("me before sign-in %d", code)
	}
	// Starting needs the custom header (no plain cross-site form posts).
	if code, _ := b.do(http.MethodPost, "/web/api/login", "", nil); code != http.StatusForbidden {
		t.Fatalf("start without header %d", code)
	}
	code := b.start()
	if b.poll() != "pending" {
		t.Fatal("not pending")
	}

	// The app sees where the browser is, then approves; lowercase and no dash work.
	info := f.alice.json(http.MethodGet, "/api/v1/me/web-login?code="+code, nil, http.StatusOK)
	if info["user_agent"] != "Go-http-client/1.1" {
		t.Fatalf("info %v", info)
	}
	typed := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	f.alice.json(http.MethodPost, "/api/v1/me/web-login", map[string]string{"code": typed}, http.StatusNoContent)
	// A second approval (another user racing) fails: the code is used.
	if c, _ := f.bob.do(http.MethodPost, "/api/v1/me/web-login", map[string]string{"code": code}); c != http.StatusNotFound {
		t.Fatalf("second approve %d", c)
	}

	// Another browser that only knows the code gets nothing.
	thief := &browser{t: t, base: f.ts.URL, cookies: map[string]string{}}
	if thief.poll() != "expired" {
		t.Fatal("thief got a status")
	}

	if s := b.poll(); s != "approved" {
		t.Fatalf("poll %s", s)
	}
	if _, ok := b.cookies["nubilo_web"]; !ok {
		t.Fatal("no session cookie")
	}
	c, out := b.do(http.MethodGet, "/web/api/me", "", nil)
	if c != http.StatusOK || !strings.Contains(string(out), `"username":"alice"`) {
		t.Fatalf("me %d %s", c, out)
	}
	// The session can only do what Alice's phone can: Bob's files stay hidden.
	bobFiles := f.files(t, f.bob)
	if c, _ := b.do(http.MethodPost, "/web/api/objects", `{"collection_id":"`+bobFiles+`"}`, webHdr); c != http.StatusNotFound {
		t.Fatalf("bob's files %d", c)
	}
	aliceFiles := f.files(t, f.alice)
	if c, out := b.do(http.MethodPost, "/web/api/objects", `{"collection_id":"`+aliceFiles+`"}`, webHdr); c != http.StatusOK {
		t.Fatalf("own files %d %s", c, out)
	}
	// State changes need the header and a same-origin Origin.
	if c, _ := b.do(http.MethodPost, "/web/api/objects", `{"collection_id":"`+aliceFiles+`"}`, nil); c != http.StatusForbidden {
		t.Fatalf("no header %d", c)
	}
	evil := map[string]string{"X-Nubilo-Web": "1", "Origin": "https://evil.example"}
	if c, _ := b.do(http.MethodPost, "/web/api/objects", `{"collection_id":"`+aliceFiles+`"}`, evil); c != http.StatusForbidden {
		t.Fatalf("cross origin %d", c)
	}

	// Download a file by object id, as an attachment.
	_, h := putBlob(f.alice, []byte("<script>alert(1)</script>"))
	res := pushCreate(f.alice, aliceFiles, h, 25)
	objID := res["results"].([]any)[0].(map[string]any)["object_id"].(string)
	req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/web/api/files/"+objID, nil)
	req.AddCookie(&http.Cookie{Name: "nubilo_web", Value: b.cookies["nubilo_web"]})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download %d %v", resp.StatusCode, resp.Header)
	}

	// The app lists and can end the session.
	sessions := f.alice.json(http.MethodGet, "/api/v1/me/web-sessions", nil, http.StatusOK)["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions %v", sessions)
	}
	if bs := f.bob.json(http.MethodGet, "/api/v1/me/web-sessions", nil, http.StatusOK)["sessions"].([]any); len(bs) != 0 {
		t.Fatalf("bob sees sessions %v", bs)
	}
	sid := sessions[0].(map[string]any)["id"].(string)
	if c, _ := f.bob.do(http.MethodDelete, "/api/v1/me/web-sessions/"+sid, nil); c != http.StatusNotFound {
		t.Fatalf("bob ended alice's session %d", c)
	}

	// Revoking the approving device ends the session.
	if err := f.ids.Revoke(context.Background(), f.alice.dev.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := b.do(http.MethodGet, "/web/api/me", "", nil); c != http.StatusUnauthorized {
		t.Fatalf("after revoke %d", c)
	}
}

func TestWebLoginOnlyAppsApprove(t *testing.T) {
	f := startFamily(t)
	b := &browser{t: t, base: f.ts.URL, cookies: map[string]string{}}
	code := b.start()
	// A DAV app password cannot sign requests, so it cannot reach the
	// approve call; an agent device is refused.
	pub, priv, _ := ncrypto.GenerateEd25519()
	agent, err := f.ids.Enroll(context.Background(), f.bob.dev.UserID, "bob-agent", pub, identity.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	s := &signer{t: t, ts: f.ts, dev: agent, priv: priv}
	if c, _ := s.do(http.MethodPost, "/api/v1/me/web-login", map[string]string{"code": code}); c != http.StatusForbidden {
		t.Fatalf("agent approve %d", c)
	}
	if b.poll() != "pending" {
		t.Fatal("approved by agent")
	}
	// Signing out ends the session for good.
	f.bob.json(http.MethodPost, "/api/v1/me/web-login", map[string]string{"code": code}, http.StatusNoContent)
	if b.poll() != "approved" {
		t.Fatal("not approved")
	}
	if c, _ := b.do(http.MethodPost, "/web/api/logout", "", webHdr); c != http.StatusNoContent {
		t.Fatalf("logout %d", c)
	}
	if len(f.bob.json(http.MethodGet, "/api/v1/me/web-sessions", nil, http.StatusOK)["sessions"].([]any)) != 0 {
		t.Fatal("session survived logout")
	}
}
