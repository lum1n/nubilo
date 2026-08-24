package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sql.DB
	Dir     string
	BlobDir string
	TmpDir  string
	BlobKey []byte
	MaxBlob int64

	writeMu sync.Mutex
}

func Open(dir, dbPath, blobDir, tmpDir string, blobKey []byte, maxBlob int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", filepath.ToSlash(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA synchronous=FULL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_meta").Scan(&n); err != nil {
		db.Close()
		return nil, err
	}
	if n == 0 {
		if _, err := db.Exec("INSERT INTO schema_meta(version) VALUES (1)"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if maxBlob <= 0 {
		maxBlob = 32 << 20
	}
	return &Store{
		DB:      db,
		Dir:     dir,
		BlobDir: blobDir,
		TmpDir:  tmpDir,
		BlobKey: append([]byte(nil), blobKey...),
		MaxBlob: maxBlob,
	}, nil
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	err := s.DB.Close()
	s.DB = nil
	return err
}

func (s *Store) WithWrite(ctx context.Context, fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.DB.PingContext(ctx)
}

func NowMS() int64 {
	return time.Now().UnixMilli()
}

func NullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

func NullString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}
