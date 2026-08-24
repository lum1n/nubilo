package store_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/store"
)

func open(t *testing.T) *store.Store {
	t.Helper()
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	dir := t.TempDir()
	master, err := ncrypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "blobs"), filepath.Join(dir, "tmp"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPutGetBlob(t *testing.T) {
	st := open(t)
	payload := []byte("photo-bytes")
	sum := ncrypto.SHA256Hex(payload)
	got, size, err := st.PutBlob(context.Background(), bytes.NewReader(payload), sum)
	if err != nil {
		t.Fatal(err)
	}
	if got != sum || size != int64(len(payload)) {
		t.Fatalf("got %s %d", got, size)
	}
	pt, err := st.GetBlobPlaintext(sum)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, payload) {
		t.Fatal("plaintext mismatch")
	}
	// idempotent put
	got2, _, err := st.PutBlob(context.Background(), bytes.NewReader(payload), sum)
	if err != nil || got2 != sum {
		t.Fatal(err)
	}
}

func TestBlobHashMismatch(t *testing.T) {
	st := open(t)
	_, _, err := st.PutBlob(context.Background(), bytes.NewReader([]byte("a")), "00"+ncrypto.SHA256Hex(nil)[2:])
	if err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestAtomicWriteLeavesNoPartialDest(t *testing.T) {
	st := open(t)
	payload := bytes.Repeat([]byte("x"), 1024)
	sum := ncrypto.SHA256Hex(payload)
	if _, _, err := st.PutBlob(context.Background(), bytes.NewReader(payload), sum); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(st.TmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("tmp not empty: %v", entries)
	}
}

func TestCorruptionDetection(t *testing.T) {
	st := open(t)
	payload := []byte("secret")
	sum := ncrypto.SHA256Hex(payload)
	if _, _, err := st.PutBlob(context.Background(), bytes.NewReader(payload), sum); err != nil {
		t.Fatal(err)
	}
	files, err := st.ListBlobFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v %v", files, err)
	}
	// overwrite ciphertext
	var path string
	_ = filepath.Walk(st.BlobDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			path = p
		}
		return nil
	})
	b, _ := os.ReadFile(path)
	b[len(b)-1] ^= 0x0f
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetBlobPlaintext(sum); err == nil {
		t.Fatal("expected hash/decrypt failure")
	}
}

func TestLeftoverTmpNotAdopted(t *testing.T) {
	st := open(t)
	if err := os.WriteFile(filepath.Join(st.TmpDir, "blob-crash.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := st.ListBlobFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("tmp leak treated as blob: %v", files)
	}
}

func TestMetadataBlobIDs(t *testing.T) {
	got := store.MetadataBlobIDs([]byte(`{"preview_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","thumb_hash":"not-a-hash","lat":1}`))
	if len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestOversizedBlob(t *testing.T) {
	st := open(t)
	st.MaxBlob = 16
	_, _, err := st.PutBlob(context.Background(), bytes.NewReader(bytes.Repeat([]byte("a"), 32)), "")
	if err == nil {
		t.Fatal("expected size error")
	}
}
