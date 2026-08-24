package crypto_test

import (
	"bytes"
	"testing"

	ncrypto "nubilo/internal/crypto"
)

func TestEncryptDecrypt(t *testing.T) {
	key, err := ncrypto.DeriveKey(bytes.Repeat([]byte{1}, 32), ncrypto.BlobKeyInfo)
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("hello personal cloud")
	enc, err := ncrypto.EncryptBlob(key, pt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc, pt) {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := ncrypto.DecryptBlob(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("got %q", got)
	}
	enc[20] ^= 0xff
	if _, err := ncrypto.DecryptBlob(key, enc); err == nil {
		t.Fatal("expected decrypt failure on tamper")
	}
}

func TestPairingCode(t *testing.T) {
	c, err := ncrypto.PairingCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 10 {
		t.Fatalf("len %d", len(c))
	}
	n := ncrypto.NormalizePairingCode(ncrypto.FormatPairingCode(c))
	if n != c {
		t.Fatalf("normalize %s vs %s", n, c)
	}
}

func TestSecretHash(t *testing.T) {
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	salt, err := ncrypto.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	h := ncrypto.HashSecret([]byte("XXXXX-YYYYY"), salt)
	if err := ncrypto.VerifySecret([]byte("XXXXX-YYYYY"), salt, h); err != nil {
		t.Fatal(err)
	}
	if err := ncrypto.VerifySecret([]byte("wrong"), salt, h); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestEd25519(t *testing.T) {
	pub, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("challenge")
	sig := ncrypto.SignEd25519(priv, msg)
	if !ncrypto.VerifyEd25519(pub, msg, sig) {
		t.Fatal("verify")
	}
	if ncrypto.VerifyEd25519(pub, []byte("other"), sig) {
		t.Fatal("should fail")
	}
}
