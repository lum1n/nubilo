package agent

import (
	"os"
	"path/filepath"
	"regexp"
)

var safeContactCacheID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (m *Map) contactCacheDir() string {
	if m == nil || m.Path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.Path), "contact-cache")
}

func (m *Map) contactCachePath(localID string) string {
	dir := m.contactCacheDir()
	if dir == "" || localID == "" {
		return ""
	}
	id := safeContactCacheID.ReplaceAllString(localID, "_")
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return filepath.Join(dir, id+".vcf")
}

func (m *Map) SaveContactCache(localID string, vcf []byte) error {
	path := m.contactCachePath(localID)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, vcf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Map) LoadContactCache(localID string) []byte {
	path := m.contactCachePath(localID)
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

func (m *Map) DeleteContactCache(localID string) {
	path := m.contactCachePath(localID)
	if path == "" {
		return
	}
	_ = os.Remove(path)
}
