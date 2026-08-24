package cli_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"nubilo/internal/cli"
)

func TestInitStatusVerify(t *testing.T) {
	dir := t.TempDir()
	if code := cli.Main([]string{"init", "--data-dir", dir, "--listen", "127.0.0.1:18443"}); code != 0 {
		t.Fatalf("init %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls.crt")); err != nil {
		t.Fatal("init should auto-write tls.crt")
	}
	if _, err := os.Stat(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	if code := cli.Main([]string{"status", "--data-dir", dir, "--json"}); code != 0 {
		t.Fatalf("status %d", code)
	}
	if code := cli.Main([]string{"verify", "--data-dir", dir}); code != 0 {
		t.Fatalf("verify %d", code)
	}
	if code := cli.Main([]string{"devices", "list", "--data-dir", dir}); code != 0 {
		t.Fatalf("devices %d", code)
	}
	if code := cli.Main([]string{"calendars", "list", "--data-dir", dir}); code != 0 {
		t.Fatalf("calendars %d", code)
	}
	if code := cli.Main([]string{"contacts", "list", "--data-dir", dir}); code != 0 {
		t.Fatalf("contacts %d", code)
	}
	if code := cli.Main([]string{"photos", "list", "--data-dir", dir}); code != 0 {
		t.Fatalf("photos %d", code)
	}
	if code := cli.Main([]string{"gc", "--data-dir", dir}); code != 0 {
		t.Fatalf("gc %d", code)
	}
	if code := cli.Main([]string{"tls", "--data-dir", dir, "--listen", "127.0.0.1:18443"}); code != 0 {
		t.Fatalf("tls %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls.crt")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "darwin" {
		if code := cli.Main([]string{"agent"}); code != 2 {
			t.Fatalf("agent on linux want 2 got %d", code)
		}
	}
}
