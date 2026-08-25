package ui_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"nubilo/internal/agent"
	"nubilo/internal/ui"
)

func agentSessionCookie(t *testing.T, srv *ui.AgentServer, base string) *http.Cookie {
	t.Helper()
	u, err := url.Parse(srv.SessionURL())
	if err != nil {
		t.Fatal(err)
	}
	sess := u.Query().Get("session")
	req, _ := http.NewRequest("GET", base+"/?session="+sess, nil)
	// Use handler directly: SessionURL host is listen addr, not test server.
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	res := w.Result()
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == "nubilo_ui" {
			return c
		}
	}
	// Redirect response should Set-Cookie
	if res.StatusCode == http.StatusFound {
		for _, c := range res.Cookies() {
			if c.Name == "nubilo_ui" {
				return c
			}
		}
	}
	t.Fatalf("no session cookie (status=%d)", res.StatusCode)
	return nil
}

func TestAgentUISelectionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv, err := ui.NewAgent(dir, ui.DefaultAgentListen, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cookie := agentSessionCookie(t, srv, ts.URL)

	req, _ := http.NewRequest("GET", ts.URL+"/api/selection", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get selection %d", resp.StatusCode)
	}
	var sel agent.Selection
	if err := json.NewDecoder(resp.Body).Decode(&sel); err != nil {
		t.Fatal(err)
	}
	sel.SyncContacts = true
	sel.IntervalSeconds = 90
	sel.WindowDays = 365
	sel.Photos.Enabled = true
	sel.Photos.Source = "albums"
	sel.SelectAlbum("person:test-pet")
	sel.SelectCalendar("cal-1", "Work")
	sel.Files.Enabled = true
	folder := filepath.Join(dir, "docs")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	sel.AddFileFolder(folder, "docs")

	body, _ := json.Marshal(sel)
	req, _ = http.NewRequest("PUT", ts.URL+"/api/selection", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put selection %d %s", resp.StatusCode, ioReadAll(resp))
	}

	got, err := agent.LoadSelection(filepath.Join(dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.SyncContacts || got.IntervalSeconds != 90 || got.WindowDays != 365 {
		t.Fatalf("selection %#v", got)
	}
	if !got.Photos.Enabled || got.Photos.Source != "albums" || len(got.Photos.Albums) != 1 || got.Photos.Albums[0] != "person:test-pet" {
		t.Fatalf("photos %#v", got.Photos)
	}
	if len(got.Calendars) != 1 || got.Calendars[0].LocalID != "cal-1" {
		t.Fatalf("calendars %#v", got.Calendars)
	}
	if !got.Files.Enabled || len(got.Files.Folders) != 1 {
		t.Fatalf("files %#v", got.Files)
	}

	req, _ = http.NewRequest("GET", ts.URL+"/api/overview", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("overview %d", resp.StatusCode)
	}
	var ov map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&ov); err != nil {
		t.Fatal(err)
	}
	if ov["data_dir"] != dir {
		t.Fatalf("data_dir %v", ov["data_dir"])
	}
}

func TestAgentUIUnauthorized(t *testing.T) {
	dir := t.TempDir()
	srv, err := ui.NewAgent(dir, "127.0.0.1:8788", nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/api/selection")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestAgentUIAlbumToggle(t *testing.T) {
	dir := t.TempDir()
	srv, err := ui.NewAgent(dir, ui.DefaultAgentListen, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cookie := agentSessionCookie(t, srv, ts.URL)

	body, _ := json.Marshal(map[string]string{"id": "person:abc"})
	req, _ := http.NewRequest("POST", ts.URL+"/api/albums/select", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("select %d", resp.StatusCode)
	}
	sel, err := agent.LoadSelection(filepath.Join(dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Photos.Albums) != 1 || sel.Photos.Albums[0] != "person:abc" {
		t.Fatalf("%#v", sel.Photos.Albums)
	}

	req, _ = http.NewRequest("POST", ts.URL+"/api/albums/unselect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sel, _ = agent.LoadSelection(filepath.Join(dir, "agent.json"))
	if len(sel.Photos.Albums) != 0 {
		t.Fatalf("still selected %#v", sel.Photos.Albums)
	}
}

func ioReadAll(resp *http.Response) string {
	b := new(bytes.Buffer)
	_, _ = b.ReadFrom(resp.Body)
	return b.String()
}
