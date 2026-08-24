package crypto_test

import (
	"testing"

	ncrypto "nubilo/internal/crypto"
)

func FuzzNormalizePairingCode(f *testing.F) {
	f.Add("XXXXX-XXXXX")
	f.Add("")
	f.Add("abcde-fghij")
	f.Fuzz(func(t *testing.T, s string) {
		n := ncrypto.NormalizePairingCode(s)
		if len(n) > len(s)+8 {
			t.Fatalf("grew unexpectedly %d -> %d", len(s), len(n))
		}
		_ = ncrypto.FormatPairingCode(n)
	})
}
