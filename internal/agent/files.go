package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"nubilo/internal/dav"
)

const maxFileBytes = 64 << 20

type LocalFile struct {
	AbsPath string
	RelPath string // slash-separated path relative to selected folder root
	Name    string // basename (DAV object name)
	Size    int64
	Data    []byte
}

// FileSource lists and writes files under selected local folders.
type FileSource interface {
	ListFiles(root string) ([]LocalFile, error)
	WriteFile(absPath string, data []byte) error
	DeleteFile(absPath string) error
	MkdirAll(absPath string) error
}

type localFiles struct{}

func OpenFiles() FileSource { return localFiles{} }

func (localFiles) ListFiles(root string) ([]LocalFile, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New("not a directory")
	}
	var out []LocalFile
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if name == ".git" || name == ".DS_Store" || name == "node_modules" {
			if d.IsDir() && name != ".DS_Store" {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				return nil
			}
		}
		if strings.HasPrefix(name, ".") && name != "." && name != ".." {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.Contains(rel, "..") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, LocalFile{
			AbsPath: path,
			RelPath: rel,
			Name:    filepath.Base(path),
			Size:    int64(len(data)),
			Data:    data,
		})
		return nil
	})
	return out, err
}

func (localFiles) WriteFile(absPath string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return err
	}
	tmp := absPath + ".nubilo-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, absPath)
}

func (localFiles) DeleteFile(absPath string) error {
	err := os.Remove(absPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (localFiles) MkdirAll(absPath string) error {
	return os.MkdirAll(absPath, 0o700)
}

func skipFileName(name string) bool {
	return name == "" || name == "." || name == ".." || !dav.ValidDisplayName(name)
}
