package photos_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/photos"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

// BenchmarkLibraryIndex12k times the library index for a 12,000 photo
// library and reports its size on the wire.
func BenchmarkLibraryIndex12k(b *testing.B) {
	dir := b.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 8<<20)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	ctx := context.Background()
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, err := identity.NewService(st).Enroll(ctx, "", "cli", pub, identity.RoleClient)
	if err != nil {
		b.Fatal(err)
	}
	svc := photos.Service{Engine: syncengine.New(st), Store: st, Opt: photos.DefaultOptions()}
	col, err := svc.Library(ctx, dev.UserID)
	if err != nil {
		b.Fatal(err)
	}
	tx, _ := st.DB.Begin()
	for i := 0; i < 12000; i++ {
		h := ncrypto.SHA256Hex([]byte(fmt.Sprint("o", i)))
		m := photos.Meta{
			Name: fmt.Sprintf("IMG_%04d.HEIC", i), MIME: "image/heic", Kind: "image", Width: 4032, Height: 3024,
			CameraMake: "Apple", CameraModel: "iPhone 15 Pro", TakenAtMS: 1_600_000_000_000 + int64(i)*3_600_000,
			Checksum: h, ThumbHash: ncrypto.SHA256Hex([]byte(fmt.Sprint("t", i))), PreviewHash: ncrypto.SHA256Hex([]byte(fmt.Sprint("p", i))),
			Perceptual: "a1b2c3d4e5f60718",
		}
		if _, err := tx.Exec(`INSERT INTO objects(id, collection_id, kind, revision, content_hash, metadata_hash, blob_id, size, metadata, created_at, updated_at)
			VALUES (?, ?, 'photo', 1, ?, '', ?, 3000000, ?, 0, 0)`, ids.New(), col.ID, h, h, string(photos.EncodeMeta(m))); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	var raw, zipped int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, err := svc.LibraryIndex(ctx, dev.UserID)
		if err != nil || len(entries) != 12000 {
			b.Fatalf("%d %v", len(entries), err)
		}
		body, _ := json.Marshal(map[string]any{"photos": entries})
		var buf bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		_, _ = gz.Write(body)
		_ = gz.Close()
		raw, zipped = len(body), buf.Len()
	}
	b.ReportMetric(float64(raw)/1e6, "MB-json")
	b.ReportMetric(float64(zipped)/1e6, "MB-gzip")
}
