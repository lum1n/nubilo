package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ncrypto "nubilo/internal/crypto"
)

func blobPath(root, hash string) string {
	hash = strings.ToLower(hash)
	if len(hash) < 4 {
		return filepath.Join(root, hash)
	}
	return filepath.Join(root, hash[:2], hash[2:4], hash)
}

func (s *Store) PutBlob(ctx context.Context, r io.Reader, expectedHash string) (hash string, size int64, err error) {
	expectedHash = strings.ToLower(strings.TrimSpace(expectedHash))
	tmp, err := os.CreateTemp(s.TmpDir, "blob-*.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	limited := io.LimitReader(r, s.MaxBlob+1)
	plain, err := io.ReadAll(limited)
	if err != nil {
		return "", 0, err
	}
	if int64(len(plain)) > s.MaxBlob {
		return "", 0, fmt.Errorf("store: blob exceeds max size %d", s.MaxBlob)
	}
	sum := ncrypto.SHA256Hex(plain)
	if expectedHash != "" && sum != expectedHash {
		return "", 0, fmt.Errorf("store: blob hash mismatch")
	}
	enc, err := ncrypto.EncryptBlob(s.BlobKey, plain)
	if err != nil {
		return "", 0, err
	}
	if _, err := tmp.Write(enc); err != nil {
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}

	dest := blobPath(s.BlobDir, sum)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", 0, err
	}
	if _, err := os.Stat(dest); err == nil {
		// identical plaintext already stored
		if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
			return "", 0, err
		}
		tmpName = "" // prevent double remove
		return sum, int64(len(plain)), s.ensureBlobRow(ctx, sum, int64(len(plain)))
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", 0, err
	}
	tmpName = ""
	if dir, err := os.Open(filepath.Dir(dest)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	if err := s.ensureBlobRow(ctx, sum, int64(len(plain))); err != nil {
		return "", 0, err
	}
	return sum, int64(len(plain)), nil
}

func (s *Store) ensureBlobRow(ctx context.Context, hash string, size int64) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO blobs(id, size, refcount, created_at)
		VALUES (?, ?, 0, ?)
		ON CONFLICT(id) DO NOTHING
	`, hash, size, NowMS())
	return err
}

func (s *Store) GetBlobPlaintext(hash string) ([]byte, error) {
	dest := blobPath(s.BlobDir, strings.ToLower(hash))
	enc, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	pt, err := ncrypto.DecryptBlob(s.BlobKey, enc)
	if err != nil {
		return nil, err
	}
	if ncrypto.SHA256Hex(pt) != strings.ToLower(hash) {
		return nil, fmt.Errorf("store: blob plaintext hash mismatch")
	}
	return pt, nil
}

func (s *Store) BlobPath(hash string) string {
	return blobPath(s.BlobDir, strings.ToLower(hash))
}

func (s *Store) BlobExists(hash string) bool {
	_, err := os.Stat(s.BlobPath(hash))
	return err == nil
}

func (s *Store) RemoveBlob(ctx context.Context, hash string) error {
	hash = strings.ToLower(hash)
	_ = os.Remove(s.BlobPath(hash))
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM blob_uploads WHERE blob_id = ?`, hash); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, hash)
	return err
}

func (s *Store) ListBlobFiles() ([]string, error) {
	var hashes []string
	err := filepath.Walk(s.BlobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		h := filepath.Base(path)
		if len(h) == 64 {
			hashes = append(hashes, strings.ToLower(h))
		}
		return nil
	})
	return hashes, err
}

// BlobStats returns count and total size from the metadata table (no disk walk).
func (s *Store) BlobStats(ctx context.Context) (count int, bytes int64, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM blobs`).Scan(&count, &bytes)
	return count, bytes, err
}

func (s *Store) IncBlobRef(tx *sql.Tx, hash string) error {
	if hash == "" {
		return nil
	}
	res, err := tx.Exec(`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, hash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("store: unknown blob %s", hash)
	}
	return nil
}

func (s *Store) DecBlobRef(tx *sql.Tx, hash string) error {
	if hash == "" {
		return nil
	}
	_, err := tx.Exec(`UPDATE blobs SET refcount = MAX(refcount - 1, 0) WHERE id = ?`, hash)
	return err
}

func (s *Store) RecordUpload(ctx context.Context, blobID, deviceID string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO blob_uploads(blob_id, device_id, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(blob_id, device_id) DO UPDATE SET created_at = excluded.created_at
	`, blobID, deviceID, NowMS())
	return err
}
