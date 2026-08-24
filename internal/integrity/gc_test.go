package integrity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/integrity"
	"nubilo/internal/photos"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

func gcEnv(t *testing.T) (*store.Store, *syncengine.Engine, *identity.Device) {
	t.Helper()
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := syncengine.New(st)
	idsvc := identity.NewService(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, err := idsvc.Enroll(context.Background(), "agent", pub, identity.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	return st, eng, dev
}

func TestGCKeepsPhotoDerivatives(t *testing.T) {
	st, eng, dev := gcEnv(t)
	svc := photos.Service{Engine: eng, Store: st, Opt: photos.DefaultOptions()}
	img := testJPEGBytes(t)
	obj, err := svc.Ingest(context.Background(), dev, img, "keep.jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	orphan := []byte("not-a-photo-blob")
	oh := ncrypto.SHA256Hex(orphan)
	if _, _, err := st.PutBlob(context.Background(), bytes.NewReader(orphan), oh); err != nil {
		t.Fatal(err)
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("%v", issues)
	}
	rep, err := integrity.Collect(context.Background(), st, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.BlobsRemoved != 1 {
		t.Fatalf("expected 1 unused blob removed, got %+v", rep)
	}
	m := photos.ParseMeta(obj.Metadata)
	for _, h := range []string{obj.BlobID, m.PreviewHash, m.ThumbHash} {
		if !st.BlobExists(h) {
			t.Fatalf("live blob missing after gc: %s", h)
		}
	}
}

func TestTombstoneCompactAfterAck(t *testing.T) {
	st, eng, dev := gcEnv(t)
	col, err := eng.CreateCollection(context.Background(), "files", "f", "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("gone")
	sum := ncrypto.SHA256Hex(payload)
	if _, _, err := st.PutBlob(context.Background(), bytes.NewReader(payload), sum); err != nil {
		t.Fatal(err)
	}
	oid := ids.New()
	res, err := eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: oid, CollectionID: col.ID, Op: syncengine.OpCreate, ContentHash: sum, BlobID: sum, Size: 4, Metadata: json.RawMessage(`{}`),
	}})
	if err != nil || res[0].Status != "ok" {
		t.Fatalf("%v %+v", err, res)
	}
	res, err = eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: oid, CollectionID: col.ID, Op: syncengine.OpDelete, BaseRevision: 1,
	}})
	if err != nil || res[0].Status != "ok" {
		t.Fatalf("delete %v %+v", err, res)
	}
	head, _ := eng.HeadSeq(context.Background())
	dry, err := integrity.Collect(context.Background(), st, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.CompactableTombstones) != 0 {
		t.Fatalf("unacked device must block compact: %+v", dry)
	}
	if err := eng.Ack(context.Background(), dev.ID, head); err != nil {
		t.Fatal(err)
	}
	rep, err := integrity.Collect(context.Background(), st, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TombstonesCompacted != 1 {
		t.Fatalf("expected tombstone compact %+v", rep)
	}
	if _, err := eng.GetObject(context.Background(), oid); err == nil {
		t.Fatal("tombstone still present")
	}
}

func TestTombstoneBlockedByLaggingDevice(t *testing.T) {
	st, eng, dev := gcEnv(t)
	idsvc := identity.NewService(st)
	pub2, _, _ := ncrypto.GenerateEd25519()
	if _, err := idsvc.Enroll(context.Background(), "other", pub2, identity.RoleClient); err != nil {
		t.Fatal(err)
	}
	col, _ := eng.CreateCollection(context.Background(), "files", "f", "", json.RawMessage(`{}`))
	sum := ncrypto.SHA256Hex([]byte("x"))
	_, _, _ = st.PutBlob(context.Background(), bytes.NewReader([]byte("x")), sum)
	oid := ids.New()
	eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: oid, CollectionID: col.ID, Op: syncengine.OpCreate, ContentHash: sum, BlobID: sum, Size: 1, Metadata: json.RawMessage(`{}`),
	}})
	eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: oid, CollectionID: col.ID, Op: syncengine.OpDelete, BaseRevision: 1,
	}})
	head, _ := eng.HeadSeq(context.Background())
	_ = eng.Ack(context.Background(), dev.ID, head)
	rep, err := integrity.Collect(context.Background(), st, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TombstonesCompacted != 0 {
		t.Fatalf("lagging device must block compact %+v", rep)
	}
}

func TestStorageCorruptionDetected(t *testing.T) {
	st, eng, dev := gcEnv(t)
	col, _ := eng.CreateCollection(context.Background(), "files", "f", "", json.RawMessage(`{}`))
	payload := []byte("payload")
	sum := ncrypto.SHA256Hex(payload)
	_, _, _ = st.PutBlob(context.Background(), bytes.NewReader(payload), sum)
	eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: col.ID, Op: syncengine.OpCreate, ContentHash: sum, BlobID: sum, Size: int64(len(payload)), Metadata: json.RawMessage(`{}`),
	}})
	path := st.BlobPath(sum)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		if i.Kind == "hash_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected hash_mismatch, got %v", issues)
	}
}

func TestTruncatedBlobDetected(t *testing.T) {
	st, _, _ := gcEnv(t)
	sum := ncrypto.SHA256Hex([]byte("abc"))
	_, _, _ = st.PutBlob(context.Background(), bytes.NewReader([]byte("abc")), sum)
	if err := os.WriteFile(st.BlobPath(sum), []byte("xx"), 0o600); err != nil {
		t.Fatal(err)
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 {
		t.Fatal("expected issue")
	}
}

func TestRepairRefcounts(t *testing.T) {
	st, eng, dev := gcEnv(t)
	col, _ := eng.CreateCollection(context.Background(), "files", "f", "", json.RawMessage(`{}`))
	sum := ncrypto.SHA256Hex([]byte("r"))
	_, _, _ = st.PutBlob(context.Background(), bytes.NewReader([]byte("r")), sum)
	eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: col.ID, Op: syncengine.OpCreate, ContentHash: sum, BlobID: sum, Size: 1, Metadata: json.RawMessage(`{}`),
	}})
	if _, err := st.DB.Exec(`UPDATE blobs SET refcount = 99 WHERE id=?`, sum); err != nil {
		t.Fatal(err)
	}
	n, err := integrity.RepairRefcounts(context.Background(), st)
	if err != nil || n != 1 {
		t.Fatalf("repair %d %v", n, err)
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil || len(issues) != 0 {
		t.Fatalf("%v %v", issues, err)
	}
}

func testJPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 10, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
