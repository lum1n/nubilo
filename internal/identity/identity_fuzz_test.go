package identity_test

import (
	"testing"

	"nubilo/internal/identity"
)

func FuzzSanitizeName(f *testing.F) {
	f.Add("Studio Mac")
	f.Add("")
	f.Add("x\x00y")
	f.Fuzz(func(t *testing.T, name string) {
		_, _ = identity.SanitizeName(name)
	})
}
