package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManifestArtifactLookup(t *testing.T) {
	m := Manifest{Artifacts: []Artifact{
		{OS: "linux", Arch: "amd64", URL: "https://x/a"},
		{OS: "darwin", Arch: "arm64", URL: "https://x/b"},
	}}
	if a, ok := m.Artifact("darwin", "arm64"); !ok || a.URL != "https://x/b" {
		t.Fatalf("darwin/arm64: %v %v", a, ok)
	}
	if _, ok := m.Artifact("linux", "arm64"); ok {
		t.Fatal("os and arch must both match")
	}
	if _, ok := (Manifest{}).Artifact("linux", "amd64"); ok {
		t.Fatal("empty manifest has no artifacts")
	}
}

func TestSourceLatestErrors(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	serve := func(h http.HandlerFunc) Source {
		ts := httptest.NewTLSServer(h)
		t.Cleanup(ts.Close)
		return Source{ManifestURL: ts.URL + "/manifest.json", Keys: []ed25519.PublicKey{pub}, HTTP: ts.Client()}
	}

	notFound := serve(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if _, err := notFound.Latest(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("404: %v", err)
	}

	broken := serve(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	if _, err := broken.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("500: %v", err)
	}

	huge := serve(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 1<<20+1)))
	})
	if _, err := huge.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized manifest: %v", err)
	}

	// A manifest signed by nobody we trust is rejected.
	unsigned := serve(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			w.Write([]byte("bm90IGEgc2lnbmF0dXJl"))
			return
		}
		w.Write([]byte(`{"version":"9.9.9","artifacts":[]}`))
	})
	if _, err := unsigned.Latest(context.Background()); !errors.Is(err, ErrSignature) {
		t.Fatalf("bad signature: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := unsigned.Latest(cancelled); err == nil {
		t.Fatal("cancelled context must fail")
	}
}

func TestDownloadRejectsBadArtifacts(t *testing.T) {
	bin := []byte("#!/bin/sh\necho new\n")
	src, m := releaseServer(t, "0.8.0", bin)
	good, _ := m.Artifact(runtime.GOOS, runtime.GOARCH)

	leftovers := func(dir string) {
		t.Helper()
		if es, _ := os.ReadDir(dir); len(es) != 0 {
			t.Fatalf("failed download left files behind: %v", es)
		}
	}

	for name, mut := range map[string]func(*Artifact){
		"zero size":     func(a *Artifact) { a.Size = 0 },
		"negative size": func(a *Artifact) { a.Size = -1 },
		"absurd size":   func(a *Artifact) { a.Size = maxBinary + 1 },
		"size too big":  func(a *Artifact) { a.Size = int64(len(bin)) + 1 },
		"size too small": func(a *Artifact) {
			a.Size = int64(len(bin)) - 1
		},
		"missing file": func(a *Artifact) { a.URL = strings.TrimSuffix(a.URL, "/nubilo") + "/nope" },
	} {
		a := good
		mut(&a)
		dir := t.TempDir()
		if _, err := src.Download(context.Background(), a, dir, nil); err == nil {
			t.Errorf("%s: download succeeded", name)
		}
		leftovers(dir)
	}

	// The hash check is case-insensitive.
	a := good
	a.SHA256 = strings.ToUpper(a.SHA256)
	path, err := src.Download(context.Background(), a, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("upper-case sha: %v", err)
	}
	if filepath.Dir(path) == "" {
		t.Fatal("no path")
	}
}
