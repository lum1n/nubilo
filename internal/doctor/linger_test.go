package doctor

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestLingerEnabledReadsLogindFile(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	old := lingerDir
	lingerDir = dir
	t.Cleanup(func() { lingerDir = old })
	if err := os.WriteFile(filepath.Join(dir, u.Username), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	on, err := lingerEnabled()
	if err != nil || !on {
		t.Fatalf("linger = %v, %v; want enabled", on, err)
	}
	r := &Report{}
	checkLinger(r)
	if len(r.Checks) != 1 || r.Checks[0].Status != OK {
		t.Fatalf("%+v", r.Checks)
	}
}
