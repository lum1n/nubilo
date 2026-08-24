package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/integrity"
	"nubilo/internal/store"
)

const magic = "NUBI"

type Manifest struct {
	Version   int   `json:"version"`
	CreatedAt int64 `json:"created_at"`
	BlobCount int   `json:"blob_count"`
}

func Create(ctx context.Context, st *store.Store, dataDir, dest, passphrase string) error {
	if passphrase == "" {
		return errors.New("backup: passphrase required")
	}
	tmp, err := os.MkdirTemp("", "nubilo-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	snap := filepath.Join(tmp, "metadata.db")
	if _, err := st.DB.ExecContext(ctx, `VACUUM INTO ?`, snap); err != nil {
		return fmt.Errorf("backup: snapshot: %w", err)
	}

	blobDir := filepath.Join(tmp, "blobs")
	if err := copyDir(st.BlobDir, blobDir); err != nil {
		return err
	}
	for _, name := range []string{"master.key", "server.key", "config.json"} {
		src := filepath.Join(dataDir, name)
		if _, err := os.Stat(src); err == nil {
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(tmp, name), b, 0o600); err != nil {
				return err
			}
		}
	}
	nblobs := 0
	_ = filepath.Walk(blobDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			nblobs++
		}
		return nil
	})
	man, err := json.Marshal(Manifest{Version: 1, CreatedAt: time.Now().UnixMilli(), BlobCount: nblobs})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), man, 0o600); err != nil {
		return err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := filepath.Walk(tmp, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(tmp, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: rel, Mode: 0o600, Size: int64(len(b)), ModTime: info.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	}); err != nil {
		tw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	salt, err := ncrypto.NewSalt()
	if err != nil {
		return err
	}
	key := ncrypto.HashSecret([]byte(passphrase), salt)
	enc, err := ncrypto.EncryptBlob(key, buf.Bytes())
	if err != nil {
		return err
	}
	out := make([]byte, 0, 4+1+2+len(salt)+len(enc))
	out = append(out, magic...)
	out = append(out, 1) // version
	var slen [2]byte
	binary.BigEndian.PutUint16(slen[:], uint16(len(salt)))
	out = append(out, slen[:]...)
	out = append(out, salt...)
	out = append(out, enc...)
	return os.WriteFile(dest, out, 0o600)
}

func Restore(ctx context.Context, archive, destDir, passphrase string) error {
	if passphrase == "" {
		return errors.New("backup: passphrase required")
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	pt, err := decryptArchive(raw, passphrase)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return errors.New("backup: restore destination must be empty")
	}
	tr := tar.NewReader(bytes.NewReader(pt))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		path, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func decryptArchive(raw []byte, passphrase string) ([]byte, error) {
	if len(raw) < 7 || string(raw[:4]) != magic {
		return nil, errors.New("backup: not a nubilo archive")
	}
	if raw[4] != 1 {
		return nil, errors.New("backup: unsupported version")
	}
	slen := binary.BigEndian.Uint16(raw[5:7])
	if int(7+slen) > len(raw) {
		return nil, errors.New("backup: truncated")
	}
	salt := raw[7 : 7+slen]
	enc := raw[7+slen:]
	key := ncrypto.HashSecret([]byte(passphrase), salt)
	return ncrypto.DecryptBlob(key, enc)
}

func VerifyRestore(ctx context.Context, archive, passphrase string, blobKeyCheck func(*store.Store) error) ([]integrity.Issue, error) {
	dir, err := os.MkdirTemp("", "nubilo-restore-verify-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := Restore(ctx, archive, dir, passphrase); err != nil {
		return nil, err
	}
	master, err := ncrypto.ReadKeyFile(filepath.Join(dir, "master.key"), ncrypto.MasterKeySize)
	if err != nil {
		return nil, err
	}
	blobKey, err := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dir, filepath.Join(dir, "metadata.db"), filepath.Join(dir, "blobs"), filepath.Join(dir, "tmp"), blobKey, 64<<20)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if blobKeyCheck != nil {
		if err := blobKeyCheck(st); err != nil {
			return nil, err
		}
	}
	return integrity.Check(ctx, st)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean("/" + name)
	rel := strings.TrimPrefix(filepath.ToSlash(clean), "/")
	if rel == "" || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return "", errors.New("backup: path traversal in archive")
	}
	joined := filepath.Join(root, filepath.FromSlash(rel))
	relToRoot, err := filepath.Rel(root, joined)
	if err != nil || strings.HasPrefix(relToRoot, "..") {
		return "", errors.New("backup: path traversal in archive")
	}
	return joined, nil
}
