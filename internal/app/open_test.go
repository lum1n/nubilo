package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"nubilo/internal/app"
)

func TestOpenDoesNotCreateMissingDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".nubilo")
	if _, err := app.Open(dir); !errors.Is(err, app.ErrNotInitialized) {
		t.Fatalf("got %v, want ErrNotInitialized", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created %s (stat err %v)", dir, err)
	}
}
