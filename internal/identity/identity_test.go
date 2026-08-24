package identity_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/store"
)

func setup(t *testing.T) (*identity.Service, context.Context) {
	t.Helper()
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
	s.TTL = time.Minute
	s.MaxAttempts = 5
	s.MaxActive = 3
	return s, context.Background()
}

func TestPairingHappyPath(t *testing.T) {
	s, ctx := setup(t)
	code, _, err := s.StartPairing(ctx, identity.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	br, err := s.Begin(ctx, identity.BeginRequest{Code: ncrypto.FormatPairingCode(code), Name: "Studio Mac", PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	sig := ncrypto.SignEd25519(priv, br.Challenge)
	dev, err := s.Complete(ctx, br.PairingID, sig)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Name != "Studio Mac" || dev.Role != identity.RoleAgent {
		t.Fatalf("%+v", dev)
	}
	_, err = s.Complete(ctx, br.PairingID, sig)
	if err == nil {
		t.Fatal("code should be single-use")
	}
}

func TestPairingWrongCode(t *testing.T) {
	s, ctx := setup(t)
	_, _, err := s.StartPairing(ctx, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ncrypto.GenerateEd25519()
	_, err = s.Begin(ctx, identity.BeginRequest{Code: "AAAAA-AAAAA", Name: "x", PublicKey: pub})
	if err == nil {
		t.Fatal("expected invalid code")
	}
}

func TestPairingBadSignature(t *testing.T) {
	s, ctx := setup(t)
	code, _, _ := s.StartPairing(ctx, identity.RoleClient)
	pub, _, _ := ncrypto.GenerateEd25519()
	_, priv2, _ := ncrypto.GenerateEd25519()
	br, err := s.Begin(ctx, identity.BeginRequest{Code: code, Name: "phone", PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	sig := ncrypto.SignEd25519(priv2, br.Challenge)
	if _, err := s.Complete(ctx, br.PairingID, sig); err == nil {
		t.Fatal("expected bad signature")
	}
}

func TestRevoke(t *testing.T) {
	s, ctx := setup(t)
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, err := s.Enroll(ctx, "laptop", pub, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Revoked() {
		t.Fatal("expected revoked")
	}
	other, _, _ := ncrypto.GenerateEd25519()
	dev2, err := s.Enroll(ctx, "other", other, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	if dev2.Revoked() {
		t.Fatal("other device should be intact")
	}
}

func TestTooManyActive(t *testing.T) {
	s, ctx := setup(t)
	s.MaxActive = 1
	if _, _, err := s.StartPairing(ctx, identity.RoleClient); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartPairing(ctx, identity.RoleClient); err == nil {
		t.Fatal("expected too many active")
	}
}

func TestMaxAttemptsBegin(t *testing.T) {
	s, ctx := setup(t)
	s.MaxAttempts = 2
	code, _, _ := s.StartPairing(ctx, identity.RoleClient)
	pub, _, _ := ncrypto.GenerateEd25519()
	for i := 0; i < 2; i++ {
		if _, err := s.Begin(ctx, identity.BeginRequest{Code: code, Name: "n", PublicKey: pub}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Begin(ctx, identity.BeginRequest{Code: code, Name: "n", PublicKey: pub}); err == nil {
		t.Fatal("expected too many tries")
	}
}

func TestExpiredPairing(t *testing.T) {
	s, ctx := setup(t)
	s.TTL = time.Millisecond
	code, _, _ := s.StartPairing(ctx, identity.RoleClient)
	time.Sleep(5 * time.Millisecond)
	pub, _, _ := ncrypto.GenerateEd25519()
	_, err := s.Begin(ctx, identity.BeginRequest{Code: code, Name: "n", PublicKey: pub})
	if err == nil {
		t.Fatal("expected expired")
	}
}

func TestRenameSanitize(t *testing.T) {
	s, ctx := setup(t)
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, _ := s.Enroll(ctx, "a", pub, identity.RoleClient)
	if err := s.Rename(ctx, dev.ID, "  "); err == nil {
		t.Fatal("empty name")
	}
}
