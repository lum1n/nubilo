package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nubilo/internal/config"
	"nubilo/internal/relay"
	"nubilo/internal/update"
)

func relayDirWithConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, relay.ConfigFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRelayDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NUBILO_DATA_DIR", "")
	t.Setenv("NUBILO_RELAY_DIR", "")

	// Explicit dir without relay.json: not a relay.
	if _, ok := relayDataDir(global{dataDir: t.TempDir()}); ok {
		t.Fatal("dir without relay.json should not be a relay")
	}
	// Explicit dir with relay.json: a relay.
	rd := relayDirWithConfig(t)
	if got, ok := relayDataDir(global{dataDir: rd}); !ok || got != rd {
		t.Fatalf("got %q %v", got, ok)
	}
	// Default dir from env, no home server config: a relay.
	rd2 := relayDirWithConfig(t)
	t.Setenv("NUBILO_RELAY_DIR", rd2)
	if got, ok := relayDataDir(global{}); !ok || got != rd2 {
		t.Fatalf("got %q %v", got, ok)
	}
	// A home server config on the same machine wins when no dir is given...
	srv := filepath.Join(home, ".nubilo")
	if err := os.MkdirAll(srv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Paths(srv).Config, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := relayDataDir(global{}); ok {
		t.Fatal("home server config should take precedence over default relay dir")
	}
	// ...but an explicit --data-dir still selects the relay.
	if _, ok := relayDataDir(global{dataDir: rd}); !ok {
		t.Fatal("explicit relay dir should be honoured")
	}
}

func TestRunRelayUpdate(t *testing.T) {
	dir := t.TempDir()
	if code := runRelayUpdate(dir, "status"); code != 0 {
		t.Fatalf("status %d", code)
	}
	if code := runRelayUpdate(dir, "check"); code != 2 {
		t.Fatalf("check %d", code)
	}
	if code := runRelayUpdate(dir, "install"); code != 2 {
		t.Fatalf("install %d", code)
	}
	if code := runRelayUpdate(dir, "bogus"); code != 2 {
		t.Fatalf("bogus %d", code)
	}
	// Rollback with nothing to roll back to must fail, not succeed silently.
	if code := runRelayUpdate(dir, "rollback"); code == 0 {
		t.Fatal("rollback with no previous binary should fail")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("a", "b") != "a" || firstNonEmpty("", "b") != "b" || firstNonEmpty("", "") != "" {
		t.Fatal("firstNonEmpty")
	}
}

func TestLocalAdmin(t *testing.T) {
	var gotMethod, gotPath, gotTok string
	status := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotTok = r.Method, r.URL.Path, r.Header.Get("X-Nubilo-Admin")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(update.Status{Current: "v1", State: "idle"})
	}))
	defer ts.Close()

	dir := t.TempDir()
	p := config.Paths(dir)
	cfg := config.Defaults(dir)
	cfg.Listen = strings.TrimPrefix(ts.URL, "http://")
	cfg.TLS.Cert = ""
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(p.Config, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.AdminToken, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var st update.Status
	if err := localAdmin(dir, http.MethodPost, "/api/v1/admin/update/check", &st); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/admin/update/check" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotTok != "secret" {
		t.Fatalf("token should be trimmed, got %q", gotTok)
	}
	if st.Current != "v1" {
		t.Fatalf("status %+v", st)
	}

	status = http.StatusForbidden
	err := localAdmin(dir, http.MethodGet, "/x", &st)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("want HTTP 403 error, got %v", err)
	}

	// Missing admin token.
	if err := os.Remove(p.AdminToken); err != nil {
		t.Fatal(err)
	}
	if err := localAdmin(dir, http.MethodGet, "/x", &st); err == nil {
		t.Fatal("missing token should fail")
	}
}

func TestPrintUpdateStatusDoesNotPanic(t *testing.T) {
	printUpdateStatus(update.Status{})
	printUpdateStatus(update.Status{
		Current: "v1", Available: &update.Release{Version: "v2"}, Blocked: "x", Auto: true,
		CheckedAt: 1, Last: &update.Outcome{From: "a", To: "b"},
		Relay: &update.RelayStatus{Reachable: false, Error: "down"},
	})
}

func TestRunUpdateUnknownSubcommand(t *testing.T) {
	dir := t.TempDir()
	if code := runUpdate(global{dataDir: dir}, []string{"bogus"}); code != 2 {
		t.Fatalf("got %d", code)
	}
}
