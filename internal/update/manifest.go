// Package update installs new nubilo releases on a running server: it
// checks the signed release manifest, downloads the binary for this
// platform, tries the new version's migrations on a snapshot of the
// database, backs up the database and wired state, swaps the binary
// (keeping the previous one), and restarts through the service manager. If
// the new version does not come up healthy, it goes back to the previous
// binary and backups.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Manifest describes one release. It is published as manifest.json next to
// the binaries, with an Ed25519 signature over its exact bytes in
// manifest.json.sig.
type Manifest struct {
	Version     string `json:"version"`
	PublishedAt int64  `json:"published_at"`
	Notes       string `json:"notes,omitempty"`
	// WiredProtocol is the wired wire protocol the binaries speak. A home
	// server and its relay must speak the same one.
	WiredProtocol string `json:"wired_protocol,omitempty"`
	WiredCommit   string `json:"wired_commit,omitempty"`
	// MinFrom is the oldest version that may update straight to this one.
	MinFrom   string     `json:"min_from,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one platform's binary.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Artifact returns the binary for goos/goarch.
func (m Manifest) Artifact(goos, goarch string) (Artifact, bool) {
	for _, a := range m.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Artifact{}, false
}

var (
	ErrSignature = errors.New("update: release signature does not verify")
	ErrManifest  = errors.New("update: malformed release manifest")
)

// Sign returns the base64 signature published as manifest.json.sig.
func Sign(manifest []byte, key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, manifest))
}

// Verify checks sig (base64, as published) over the manifest bytes with any
// of keys, and parses the manifest. Nothing in it is trusted before this.
func Verify(manifest, sig []byte, keys []ed25519.PublicKey) (Manifest, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Manifest{}, ErrSignature
	}
	ok := false
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, manifest, raw) {
			ok = true
			break
		}
	}
	if !ok {
		return Manifest{}, ErrSignature
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if _, ok := parseVersion(m.Version); !ok {
		return Manifest{}, fmt.Errorf("%w: version %q", ErrManifest, m.Version)
	}
	for _, a := range m.Artifacts {
		if a.OS == "" || a.Arch == "" || !strings.HasPrefix(a.URL, "https://") || len(a.SHA256) != 64 || a.Size <= 0 {
			return Manifest{}, fmt.Errorf("%w: artifact %s/%s", ErrManifest, a.OS, a.Arch)
		}
	}
	return m, nil
}

type semver struct {
	nums [3]int
	pre  string
}

// parseVersion reads "1.2.3" or "1.2.3-pre" (a leading v is allowed).
func parseVersion(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var s semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		s.nums[i] = n
	}
	s.pre = pre
	return s, true
}

// Compare orders versions: -1 if a < b, 0 if equal, 1 if a > b. A
// pre-release sorts before its release (0.7.0-phase8 < 0.7.0); pre-releases
// of the same version compare as strings. Unparseable versions sort first.
func Compare(a, b string) int {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range va.nums {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	case va.pre < vb.pre:
		return -1
	default:
		return 1
	}
}
