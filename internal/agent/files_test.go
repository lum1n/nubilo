package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFilesListAndWrite(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("x"), 0o600)
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o700)

	fs := OpenFiles()
	list, err := fs.ListFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list %#v", list)
	}
	byRel := map[string]LocalFile{}
	for _, f := range list {
		byRel[f.RelPath] = f
	}
	if byRel["a.txt"].Size != 5 || string(byRel["docs/b.txt"].Data) != "beta" {
		t.Fatalf("%#v", byRel)
	}

	out := filepath.Join(root, "docs", "c.txt")
	if err := fs.WriteFile(out, []byte("gamma")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "gamma" {
		t.Fatalf("%q", b)
	}
	if err := fs.DeleteFile(out); err != nil {
		t.Fatal(err)
	}
}

func TestSlashRelDir(t *testing.T) {
	if got := slashRelDir("a.txt"); got != "" {
		t.Fatalf("%q", got)
	}
	if got := slashRelDir("docs/b.txt"); got != "docs" {
		t.Fatalf("%q", got)
	}
	if got := slashRelDir("docs/nested/c.txt"); got != "docs/nested" {
		t.Fatalf("%q", got)
	}
}
