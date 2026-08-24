package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"nubilo/internal/config"
)

func TestNonLoopbackRequiresTLS(t *testing.T) {
	c := config.Defaults(t.TempDir())
	c.Listen = "0.0.0.0:8443"
	if err := c.Validate(); err == nil {
		t.Fatal("expected TLS required")
	}
	c.TLS.Cert = "c.pem"
	c.TLS.Key = "k.pem"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoopbackDefaultOK(t *testing.T) {
	c := config.Defaults(t.TempDir())
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	c := config.Defaults(dir)
	p := filepath.Join(dir, "config.json")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != c.Listen {
		t.Fatal(got.Listen)
	}
}
