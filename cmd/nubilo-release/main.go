// Command nubilo-release makes the signed manifest servers update from.
//
//	nubilo-release keygen
//	    Prints a new signing key: the private half for the
//	    NUBILO_RELEASE_KEY secret, the public half for internal/update/key.go.
//	nubilo-release manifest -version 0.8.0 -base-url URL [-notes FILE]
//	    [-wired-protocol N] [-wired-commit C] [-min-from V] -out DIR nubilo-<os>-<arch>...
//	    Writes DIR/manifest.json and, signed with $NUBILO_RELEASE_KEY,
//	    DIR/manifest.json.sig.
//	nubilo-release verify DIR
//	    Checks DIR/manifest.json against the keys compiled into nubilo, as a
//	    server would.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nubilo/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen()
	case "manifest":
		err = manifest(os.Args[2:])
	case "verify":
		if len(os.Args) != 3 {
			usage()
		}
		err = verify(os.Args[2])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nubilo-release:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nubilo-release keygen | manifest [flags] FILES... | verify DIR")
	os.Exit(2)
}

func keygen() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Println("private (NUBILO_RELEASE_KEY secret):", base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Println("public (internal/update/key.go):    ", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	version := fs.String("version", "", "release version, e.g. 0.8.0")
	baseURL := fs.String("base-url", "", "URL the files are downloaded from (ends in /)")
	notesFile := fs.String("notes", "", "release notes (Markdown file)")
	protocol := fs.String("wired-protocol", "", "wired protocol the binaries speak")
	commit := fs.String("wired-commit", "", "wired commit the binaries link")
	minFrom := fs.String("min-from", "", "oldest version that may update straight to this one")
	out := fs.String("out", ".", "directory for manifest.json and manifest.json.sig")
	_ = fs.Parse(args)
	if *version == "" || !strings.HasPrefix(*baseURL, "https://") || fs.NArg() == 0 {
		return fmt.Errorf("manifest needs -version, an https -base-url and the binaries")
	}
	key, err := signingKey()
	if err != nil {
		return err
	}
	m := update.Manifest{
		Version: strings.TrimPrefix(*version, "v"), PublishedAt: time.Now().UnixMilli(),
		WiredProtocol: *protocol, WiredCommit: *commit, MinFrom: *minFrom,
	}
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			return err
		}
		m.Notes = strings.TrimSpace(string(b))
	}
	for _, path := range fs.Args() {
		name := filepath.Base(path)
		// nubilo-<os>-<arch>
		parts := strings.Split(name, "-")
		if len(parts) != 3 || parts[0] != "nubilo" {
			return fmt.Errorf("%s: want nubilo-<os>-<arch>", name)
		}
		sum, size, err := hashFile(path)
		if err != nil {
			return err
		}
		m.Artifacts = append(m.Artifacts, update.Artifact{
			OS: parts[1], Arch: parts[2], URL: strings.TrimSuffix(*baseURL, "/") + "/" + name, SHA256: sum, Size: size,
		})
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), body, 0o644); err != nil {
		return err
	}
	sig := update.Sign(body, key)
	// The signature must check against the key servers carry, or no server
	// would install this release.
	if _, err := update.Verify(body, []byte(sig), []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "manifest.json.sig"), []byte(sig+"\n"), 0o644)
}

func verify(dir string) error {
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		return err
	}
	keys := update.ReleaseKeys()
	if len(keys) == 0 {
		return fmt.Errorf("no release keys compiled in (internal/update/key.go)")
	}
	m, err := update.Verify(body, sig, keys)
	if err != nil {
		return err
	}
	for _, a := range m.Artifacts {
		sum, size, err := hashFile(filepath.Join(dir, filepath.Base(a.URL)))
		if err != nil {
			return err
		}
		if sum != a.SHA256 || size != a.Size {
			return fmt.Errorf("%s does not match the manifest", filepath.Base(a.URL))
		}
	}
	fmt.Printf("ok: %s, %d binaries, signed with a compiled-in key\n", m.Version, len(m.Artifacts))
	return nil
}

func signingKey() (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("NUBILO_RELEASE_KEY")))
	if err != nil || len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("NUBILO_RELEASE_KEY must be a base64 Ed25519 seed (nubilo-release keygen)")
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
