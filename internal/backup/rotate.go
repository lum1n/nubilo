package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nubilo/internal/store"
)

// RotateCreate writes an encrypted backup under dir/backups and prunes older ones.
func RotateCreate(ctx context.Context, st *store.Store, dataDir, passphrase string, keep int) (string, error) {
	if keep <= 0 {
		keep = 7
	}
	outDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("nubilo-%s-%d.nuback", time.Now().UTC().Format("20060102-150405"), time.Now().UnixNano()%1_000_000)
	dest := filepath.Join(outDir, name)
	if err := Create(ctx, st, dataDir, dest, passphrase); err != nil {
		return "", err
	}
	_ = pruneBackups(outDir, keep)
	return dest, nil
}

func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".nuback") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	if len(files) <= keep {
		return nil
	}
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(f)
	}
	return nil
}

// SameDataDir reports whether dest resolves to the same path as live.
func SameDataDir(live, dest string) (bool, error) {
	a, err := filepath.Abs(live)
	if err != nil {
		return false, err
	}
	b, err := filepath.Abs(dest)
	if err != nil {
		return false, err
	}
	return a == b, nil
}
