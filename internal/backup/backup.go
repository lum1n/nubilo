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
	"syscall"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/integrity"
	"nubilo/internal/store"
)

const (
	magic     = "NUBI"
	archiveV1 = 1 // whole-tar AEAD (legacy)
	archiveV2 = 2 // chunked streaming AEAD
)

type Manifest struct {
	Version   int   `json:"version"`
	CreatedAt int64 `json:"created_at"`
	BlobCount int   `json:"blob_count"`
}

// Create writes an encrypted backup archive to dest using a streaming
// chunked format. Peak memory is O(chunk size), not O(library size).
// Staging for the SQLite snapshot uses data_dir/tmp (same volume as blobs),
// not system /tmp.
func Create(ctx context.Context, st *store.Store, dataDir, dest, passphrase string) error {
	if passphrase == "" {
		return errors.New("backup: passphrase required")
	}
	if st == nil {
		return errors.New("backup: store required")
	}

	stageRoot := filepath.Join(dataDir, "tmp")
	if strings.TrimSpace(st.TmpDir) != "" {
		stageRoot = st.TmpDir
	}
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		return fmt.Errorf("backup: stage dir %s: %w", stageRoot, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}

	// Snapshot metadata only — blobs are streamed from BlobDir.
	snap, err := os.CreateTemp(stageRoot, "nubilo-meta-*.db")
	if err != nil {
		return spaceErr(stageRoot, err)
	}
	snapPath := snap.Name()
	_ = snap.Close()
	defer os.Remove(snapPath)
	if _, err := st.DB.ExecContext(ctx, `VACUUM INTO ?`, snapPath); err != nil {
		return fmt.Errorf("backup: snapshot: %w", spaceErr(stageRoot, err))
	}

	nblobs, err := countBlobFiles(st.BlobDir)
	if err != nil {
		return err
	}
	man, err := json.Marshal(Manifest{Version: 2, CreatedAt: time.Now().UnixMilli(), BlobCount: nblobs})
	if err != nil {
		return err
	}

	salt, err := ncrypto.NewSalt()
	if err != nil {
		return err
	}
	key := ncrypto.HashSecret([]byte(passphrase), salt)

	tmpOut := dest + ".tmp"
	out, err := os.OpenFile(tmpOut, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return spaceErr(filepath.Dir(dest), err)
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(tmpOut)
		}
	}()

	hdr := make([]byte, 0, 4+1+2+len(salt))
	hdr = append(hdr, magic...)
	hdr = append(hdr, archiveV2)
	var slen [2]byte
	binary.BigEndian.PutUint16(slen[:], uint16(len(salt)))
	hdr = append(hdr, slen[:]...)
	hdr = append(hdr, salt...)
	if _, err := out.Write(hdr); err != nil {
		return spaceErr(filepath.Dir(dest), err)
	}

	encW, err := newChunkEncWriter(out, key)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(encW)

	if err := writeTarFile(tw, "manifest.json", man); err != nil {
		_ = tw.Close()
		_ = encW.Close()
		return spaceErr(filepath.Dir(dest), err)
	}
	if err := writeTarPath(tw, "metadata.db", snapPath); err != nil {
		_ = tw.Close()
		_ = encW.Close()
		return spaceErr(filepath.Dir(dest), err)
	}
	for _, name := range []string{"master.key", "server.key", "config.json"} {
		src := filepath.Join(dataDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := writeTarPath(tw, name, src); err != nil {
			_ = tw.Close()
			_ = encW.Close()
			return spaceErr(filepath.Dir(dest), err)
		}
	}
	if err := writeTarTree(tw, "blobs", st.BlobDir); err != nil {
		_ = tw.Close()
		_ = encW.Close()
		return spaceErr(filepath.Dir(dest), err)
	}
	if err := tw.Close(); err != nil {
		_ = encW.Close()
		return spaceErr(filepath.Dir(dest), err)
	}
	if err := encW.Close(); err != nil {
		return spaceErr(filepath.Dir(dest), err)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpOut, dest); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeTarFile(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func writeTarPath(tw *tar.Writer, name, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func writeTarTree(tw *tar.Writer, prefix, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(filepath.Join(prefix, rel))
		if info.IsDir() {
			return nil
		}
		return writeTarPath(tw, name, path)
	})
}

func countBlobFiles(root string) (int, error) {
	n := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	})
	return n, err
}

func spaceErr(where string, err error) error {
	if err == nil {
		return nil
	}
	if isNoSpace(err) {
		return fmt.Errorf("backup: no space left while writing under %s (uses data_dir volume, not system /tmp): %w", where, err)
	}
	return err
}

func isNoSpace(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if errors.Is(e, syscall.ENOSPC) {
			return true
		}
		var pathErr *os.PathError
		if errors.As(e, &pathErr) && pathErr.Err == syscall.ENOSPC {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "no space left")
}

func Restore(ctx context.Context, archive, destDir, passphrase string) error {
	if passphrase == "" {
		return errors.New("backup: passphrase required")
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := make([]byte, 7)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return errors.New("backup: truncated")
	}
	if string(hdr[:4]) != magic {
		return errors.New("backup: not a nubilo archive")
	}
	ver := hdr[4]
	slen := binary.BigEndian.Uint16(hdr[5:7])
	salt := make([]byte, slen)
	if _, err := io.ReadFull(f, salt); err != nil {
		return errors.New("backup: truncated")
	}
	key := ncrypto.HashSecret([]byte(passphrase), salt)

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

	var tr *tar.Reader
	switch ver {
	case archiveV1:
		enc, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		pt, err := ncrypto.DecryptBlob(key, enc)
		if err != nil {
			return err
		}
		tr = tar.NewReader(bytes.NewReader(pt))
	case archiveV2:
		dec, err := newChunkDecReader(f, key)
		if err != nil {
			return err
		}
		tr = tar.NewReader(dec)
	default:
		return errors.New("backup: unsupported version")
	}

	for {
		th, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		path, err := safeJoin(destDir, th.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
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
