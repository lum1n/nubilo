package auth_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"nubilo/internal/auth"
)

var updateVectors = flag.Bool("update-vectors", false, "rewrite apps/Nubilo/Tests/Fixtures/signing_vectors.json")

type vector struct {
	Name          string `json:"name"`
	Seed          string `json:"seed"` // base64 Ed25519 seed (32 bytes)
	DeviceID      string `json:"device_id"`
	TS            int64  `json:"ts"`
	Nonce         string `json:"nonce"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Body          string `json:"body"` // base64
	Canonical     string `json:"canonical"`
	Signature     string `json:"signature"` // base64 raw std
	Authorization string `json:"authorization"`
}

// TestSigningVectors pins the request-signing format the Swift client
// reproduces (apps/Nubilo NubiloKit RequestSigner). Ed25519 is deterministic,
// so the vectors are exact. Regenerate with -update-vectors.
func TestSigningVectors(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	cases := []struct{ name, method, path, body string }{
		{"empty-get", "GET", "/api/v1/me", ""},
		{"json-post", "post", "/sync/v1/changes", `{"since_seq":0,"limit":200}`},
		{"query", "GET", "/api/v1/photos?after=01J00000000000000000000000&limit=100", ""},
		{"utf8-body", "PUT", "/api/v1/collections/01J00000000000000000000000/shares", `{"username":"åse","access":"ro"}`},
	}
	var got []vector
	for _, c := range cases {
		const dev, ts, nonce = "01J0000000000000000000DEV1", int64(1793000000000), "0123456789abcdef0123456789abcdef"
		canon := auth.Canonical(dev, ts, nonce, c.method, c.path, []byte(c.body))
		sig := ed25519.Sign(priv, canon)
		got = append(got, vector{
			Name: c.name, Seed: base64.StdEncoding.EncodeToString(seed), DeviceID: dev, TS: ts, Nonce: nonce,
			Method: c.method, Path: c.path, Body: base64.StdEncoding.EncodeToString([]byte(c.body)),
			Canonical: string(canon), Signature: base64.RawStdEncoding.EncodeToString(sig),
			Authorization: auth.SignRequest(priv, dev, c.method, c.path, []byte(c.body), ts, nonce),
		})
	}
	path := filepath.Join("..", "..", "apps", "Nubilo", "Tests", "Fixtures", "signing_vectors.json")
	b, _ := json.MarshalIndent(got, "", "  ")
	b = append(b, '\n')
	if *updateVectors {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/auth -update-vectors)", err)
	}
	if string(want) != string(b) {
		t.Fatal("signing vectors changed; the Swift client must change too (go test ./internal/auth -update-vectors)")
	}
}
