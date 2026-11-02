package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultManifestURL is the newest release on GitHub.
const DefaultManifestURL = "https://github.com/lum1n/nubilo/releases/latest/download/manifest.json"

// manifestURL is DefaultManifestURL except in test builds of the update
// loop (-ldflags "-X nubilo/internal/update.manifestURL=...").
var manifestURL = DefaultManifestURL

// maxBinary bounds a download; a nubilo binary is ~60 MB.
const maxBinary = 512 << 20

// Source is where releases come from.
type Source struct {
	ManifestURL string
	Keys        []ed25519.PublicKey
	HTTP        *http.Client
}

// DefaultSource is GitHub Releases, checked against the compiled-in keys.
func DefaultSource() Source {
	return Source{ManifestURL: manifestURL, Keys: ReleaseKeys(), HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

func (s Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// ErrNoRelease means nothing is published yet.
var ErrNoRelease = errors.New("update: no release published")

// Latest fetches and verifies the newest release's manifest.
func (s Source) Latest(ctx context.Context) (Manifest, error) {
	body, err := s.get(ctx, s.ManifestURL, 1<<20)
	if err != nil {
		return Manifest{}, err
	}
	sig, err := s.get(ctx, s.ManifestURL+".sig", 4<<10)
	if err != nil {
		return Manifest{}, err
	}
	return Verify(body, sig, s.Keys)
}

func (s Source) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("update: %s is too large", url)
	}
	return b, nil
}

// Download fetches a into dir and returns its path, executable, after
// checking its size and SHA-256 against the signed manifest. progress, if
// set, gets the fraction downloaded.
func (s Source) Download(ctx context.Context, a Artifact, dir string, progress func(float64)) (string, error) {
	if a.Size <= 0 || a.Size > maxBinary {
		return "", fmt.Errorf("update: artifact size %d", a.Size)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("update: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: download: HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(dir, "nubilo-download-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(path)
		}
	}()
	h := sha256.New()
	w := io.MultiWriter(f, h)
	var done int64
	buf := make([]byte, 256<<10)
	r := io.LimitReader(resp.Body, a.Size+1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return "", werr
			}
			done += int64(n)
			if progress != nil {
				progress(min(float64(done)/float64(a.Size), 1))
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("update: download: %w", err)
		}
	}
	if done != a.Size {
		return "", fmt.Errorf("update: download is %d bytes, the release says %d", done, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return "", fmt.Errorf("update: download does not match the release (sha256 %s)", got)
	}
	if err := f.Chmod(0o755); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return filepath.Clean(path), nil
}
