package ui_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"nubilo/internal/app"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ui"
)

func testRuntime(t *testing.T) *app.Runtime {
	t.Helper()
	dir := t.TempDir()
	if err := app.Init(dir, ""); err != nil {
		t.Fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

func login(t *testing.T, base string, rt *app.Runtime) *http.Cookie {
	t.Helper()
	tok, err := os.ReadFile(rt.Paths.AdminToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": strings.TrimSpace(string(tok))})
	resp, err := http.Post(base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "nubilo_ui" {
			return c
		}
	}
	t.Fatal("no cookie")
	return nil
}

func TestUIOverview(t *testing.T) {
	rt := testRuntime(t)
	srv, err := ui.New(rt, ui.DefaultListen, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("info %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("overview unauth %d", resp.StatusCode)
	}

	cookie := login(t, ts.URL, rt)
	req, _ := http.NewRequest("GET", ts.URL+"/api/overview", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("overview %d %s", resp.StatusCode, b)
	}
	var o map[string]any
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	if o["data_dir"] == nil {
		t.Fatalf("%v", o)
	}
}

func TestUIRejectsNonLoopback(t *testing.T) {
	rt := testRuntime(t)
	_, err := ui.New(rt, "0.0.0.0:8787", nil)
	if err == nil {
		t.Fatal("expected error for non-loopback")
	}
}

func TestUIIndex(t *testing.T) {
	rt := testRuntime(t)
	srv, err := ui.New(rt, ui.DefaultListen, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(string(b)), "nubilo") {
		t.Fatal("missing html")
	}
}

func authedJSON(t *testing.T, base string, cookie *http.Cookie, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 && strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return resp.StatusCode, out
}

func TestUICreateCollectionAndPair(t *testing.T) {
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	rt := testRuntime(t)
	srv, err := ui.New(rt, ui.DefaultListen, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cookie := login(t, ts.URL, rt)

	code, out := authedJSON(t, ts.URL, cookie, "POST", "/api/collections", map[string]string{
		"kind": "calendar", "name": "Work",
	})
	if code != 200 {
		t.Fatalf("create %d %v", code, out)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("%v", out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "GET", "/api/collections?kind=calendar", nil)
	if code != 200 {
		t.Fatalf("list %d", code)
	}
	cols, _ := out["collections"].([]any)
	found := false
	for _, c := range cols {
		m, _ := c.(map[string]any)
		if m["id"] == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created calendar missing: %v", out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/collections/"+id+"/rename", map[string]string{"name": "Personal"})
	if code != 200 {
		t.Fatalf("rename %d %v", code, out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "DELETE", "/api/collections/"+id, nil)
	if code != 200 {
		t.Fatalf("delete %d %v", code, out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/pair", map[string]string{"role": "agent"})
	if code != 200 {
		t.Fatalf("pair %d %v", code, out)
	}
	if out["code"] == nil || out["id"] == nil {
		t.Fatalf("%v", out)
	}
	pid, _ := out["id"].(string)
	code, out = authedJSON(t, ts.URL, cookie, "GET", "/api/pair/"+pid, nil)
	if code != 200 || out["completed"] != false {
		t.Fatalf("pair status %d %v", code, out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/verify", map[string]any{})
	if code != 200 || out["ok"] != true {
		t.Fatalf("verify %d %v", code, out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/gc", map[string]any{"apply": false})
	if code != 200 {
		t.Fatalf("gc %d %v", code, out)
	}

	code, out = authedJSON(t, ts.URL, cookie, "GET", "/api/status", nil)
	if code != 200 {
		t.Fatalf("status %d %v", code, out)
	}
	if _, ok := out["blob_count"]; !ok {
		t.Fatalf("status missing blob_count: %v", out)
	}
	if _, ok := out["verify_issue_count"]; ok {
		t.Fatal("status should not run integrity.Check on every load")
	}

	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/backup", map[string]any{"passphrase": "test-pass-phrase"})
	if code != 200 {
		t.Fatalf("backup %d %v", code, out)
	}
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatalf("no token %v", out)
	}
	req, err := http.NewRequest("GET", ts.URL+"/api/backup/download/"+tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(body) < 8 {
		t.Fatalf("download %d len=%d", resp.StatusCode, len(body))
	}
	// Second download of same token must fail.
	req, _ = http.NewRequest("GET", ts.URL+"/api/backup/download/"+tok, nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("reuse token %d", resp.StatusCode)
	}

	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/tls", map[string]any{})
	if code != 200 {
		t.Fatalf("tls %d %v", code, out)
	}
	if out["cert"] == nil {
		t.Fatalf("%v", out)
	}

	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i + 1)
	}
	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/devices/enroll", map[string]any{
		"name": "ui-enroll", "pubkey": hex.EncodeToString(pub), "role": "client",
	})
	if code != 200 {
		t.Fatalf("enroll %d %v", code, out)
	}
	devID, _ := out["id"].(string)
	pub2 := make([]byte, 32)
	for i := range pub2 {
		pub2[i] = byte(100 + i)
	}
	code, out = authedJSON(t, ts.URL, cookie, "POST", "/api/devices/rotate", map[string]any{
		"id": devID, "pubkey": hex.EncodeToString(pub2),
	})
	if code != 200 {
		t.Fatalf("rotate %d %v", code, out)
	}

	// Restore onto live data_dir must be refused.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("passphrase", "x")
	_ = mw.WriteField("dest", rt.Cfg.DataDir)
	_ = mw.WriteField("confirm", "RESTORE")
	fw, _ := mw.CreateFormFile("archive", "b.nuback")
	_, _ = fw.Write(body)
	mw.Close()
	req, _ = http.NewRequest("POST", ts.URL+"/api/restore", &buf)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "live") {
		t.Fatalf("restore live %d %s", resp.StatusCode, raw)
	}
}
