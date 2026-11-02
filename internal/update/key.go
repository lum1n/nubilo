package update

import (
	"crypto/ed25519"
	"encoding/base64"
)

// releaseKeys are the public keys release manifests are signed with
// (base64). The private key is the NUBILO_RELEASE_KEY secret of the GitHub
// Actions release workflow. To rotate: add the new key here, ship a
// release signed with the old one, then sign with the new one and drop
// the old key in a later release.
var releaseKeys = []string{
	"F495E/WYbmQjKIOZRU/WNT1AOckw6Cu3N8fSsoW8hFs=", // 2026-11-02
}

// testKey is an extra key for test builds of the whole update loop, set
// with -ldflags "-X nubilo/internal/update.testKey=...". Release builds
// leave it empty.
var testKey = ""

// ReleaseKeys returns the keys a release must be signed with.
func ReleaseKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range append(releaseKeys, testKey) {
		b, err := base64.StdEncoding.DecodeString(k)
		if err == nil && len(b) == ed25519.PublicKeySize {
			out = append(out, ed25519.PublicKey(b))
		}
	}
	return out
}
