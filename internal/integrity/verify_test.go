package integrity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/integrity"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

func TestVerifyCleanAndMissingBlob(t *testing.T) {
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := syncengine.New(st)
	idsvc := identity.NewService(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, _ := idsvc.Enroll(context.Background(), "d", pub, identity.RoleAgent)
	col, _ := eng.CreateCollection(context.Background(), "files", "f", "", json.RawMessage(`{}`))
	payload := []byte("orig")
	sum := ncrypto.SHA256Hex(payload)
	_, _, err = st.PutBlob(context.Background(), bytes.NewReader(payload), sum)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Push(context.Background(), dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: col.ID, Op: syncengine.OpCreate, ContentHash: sum, BlobID: sum, Size: 4, Metadata: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("%v", issues)
	}
	// delete blob file
	_ = filepath.Walk(st.BlobDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			_ = os.Remove(path)
		}
		return nil
	})
	issues, err = integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		if i.Kind == "missing_blob" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing_blob, got %v", issues)
	}
}

func TestOrphanDetectionAndRepair(t *testing.T) {
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sum := ncrypto.SHA256Hex([]byte("orphan-payload"))
	_, _, err = st.PutBlob(context.Background(), bytes.NewReader([]byte("orphan-payload")), sum)
	if err != nil {
		t.Fatal(err)
	}
	// remove metadata row, leave file
	if _, err := st.DB.Exec(`DELETE FROM blobs WHERE id=?`, sum); err != nil {
		t.Fatal(err)
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		if i.Kind == "orphan_blob" {
			found = true
		}
	}
	if !found {
		t.Fatalf("%v", issues)
	}
	n, err := integrity.RepairOrphans(context.Background(), st)
	if err != nil || n != 1 {
		t.Fatalf("repair %d %v", n, err)
	}
}
