package setup_test

import (
	"os"
	"path/filepath"
	"testing"

	"nubilo/internal/app"
	"nubilo/internal/config"
	"nubilo/internal/setup"
)

func TestEnableAutoBackup(t *testing.T) {
	dir := t.TempDir()
	if err := app.Init(dir, "127.0.0.1:18443"); err != nil {
		t.Fatal(err)
	}
	pass, pf, err := setup.EnableAutoBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pass == "" || pf == "" {
		t.Fatalf("pass=%q file=%q", pass, pf)
	}
	b, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 10 {
		t.Fatalf("passphrase file too short")
	}
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Backup.Enabled || cfg.Backup.PassphraseFile != pf {
		t.Fatalf("%+v", cfg.Backup)
	}
	// Second call keeps existing passphrase file, returns empty passphrase.
	pass2, pf2, err := setup.EnableAutoBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pass2 != "" || pf2 != pf {
		t.Fatalf("pass2=%q pf2=%q", pass2, pf2)
	}
}
