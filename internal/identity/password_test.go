package identity_test

import (
	"context"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/store"
)

func TestDAVPassword(t *testing.T) {
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := identity.NewService(st)
	dev, pass, err := s.CreateDAVDevice(context.Background(), "Finder", "webdav")
	if err != nil {
		t.Fatal(err)
	}
	if len(dev.PublicKey) != 0 {
		t.Fatal("dav device should not have a signing key")
	}
	got, err := s.AuthenticatePassword(context.Background(), dev.ID, pass)
	if err != nil || got.ID != dev.ID {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := s.AuthenticatePassword(context.Background(), dev.ID, "nope"); err == nil {
		t.Fatal("bad password")
	}
	if err := s.Revoke(context.Background(), dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticatePassword(context.Background(), dev.ID, pass); err == nil {
		t.Fatal("revoked still works")
	}
}
