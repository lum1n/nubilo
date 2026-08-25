package syncengine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

type env struct {
	eng *syncengine.Engine
	st  *store.Store
	dev *identity.Device
	col *syncengine.Collection
	ctx context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	idsvc := identity.NewService(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, err := idsvc.Enroll(context.Background(), "agent", pub, identity.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	eng := syncengine.New(st)
	col, err := eng.CreateCollection(context.Background(), "files", "docs", "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return &env{eng: eng, st: st, dev: dev, col: col, ctx: context.Background()}
}

func push(t *testing.T, e *env, key string, items ...syncengine.ChangeInput) []syncengine.PushResult {
	t.Helper()
	res, err := e.eng.Push(e.ctx, e.dev, key, items)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func put(t *testing.T, e *env, payload []byte) string {
	t.Helper()
	sum := ncrypto.SHA256Hex(payload)
	got, _, err := e.st.PutBlob(e.ctx, bytes.NewReader(payload), sum)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCountLiveObjectsByKindNested(t *testing.T) {
	e := setup(t)
	child, err := e.eng.CreateCollection(e.ctx, "files", "sub", e.col.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	blob := put(t, e, []byte("root"))
	push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: ids.New(), CollectionID: e.col.ID, Kind: "file", Op: syncengine.OpCreate,
		ContentHash: blob, BlobID: blob, Size: 4, Metadata: json.RawMessage(`{"name":"a.txt"}`),
	})
	blob2 := put(t, e, []byte("nested!"))
	push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: ids.New(), CollectionID: child.ID, Kind: "file", Op: syncengine.OpCreate,
		ContentHash: blob2, BlobID: blob2, Size: 7, Metadata: json.RawMessage(`{"name":"b.txt"}`),
	})
	n, err := e.eng.CountLiveObjectsByKind(e.ctx, "files")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count %d want 2 (root + nested)", n)
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	blob := put(t, e, []byte("v1"))
	r := push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Kind: "file", Op: syncengine.OpCreate,
		ContentHash: blob, BlobID: blob, Size: 2, Metadata: json.RawMessage(`{"name":"a.txt"}`),
	})
	if r[0].Status != "ok" || r[0].Revision != 1 {
		t.Fatalf("%+v", r[0])
	}
	blob2 := put(t, e, []byte("v2!"))
	r = push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpUpdate, BaseRevision: 1,
		ContentHash: blob2, BlobID: blob2, Size: 3, Metadata: json.RawMessage(`{"name":"a.txt"}`),
	})
	if r[0].Status != "ok" || r[0].Revision != 2 {
		t.Fatalf("%+v", r[0])
	}
	r = push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpDelete, BaseRevision: 2,
	})
	if r[0].Status != "ok" {
		t.Fatalf("%+v", r[0])
	}
	ch, err := e.eng.GetChanges(e.ctx, e.dev, 0, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Changes) != 3 {
		t.Fatalf("changes %d", len(ch.Changes))
	}
	if ch.Changes[2].Op != syncengine.OpDelete || !ch.Changes[2].Deleted {
		t.Fatalf("%+v", ch.Changes[2])
	}
}

func TestConcurrentUpdatesConflict(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	b1 := put(t, e, []byte("a"))
	push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b1, BlobID: b1, Size: 1, Metadata: json.RawMessage(`{}`),
	})
	pub2, _, _ := ncrypto.GenerateEd25519()
	idsvc := identity.NewService(e.st)
	dev2, err := idsvc.Enroll(e.ctx, "other", pub2, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	b2 := put(t, e, []byte("b"))
	b3 := put(t, e, []byte("c"))
	key1, key2 := ids.New(), ids.New()
	var r1, r2 []syncengine.PushResult
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r1, e1 = e.eng.Push(e.ctx, e.dev, key1, []syncengine.ChangeInput{{
			ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpUpdate, BaseRevision: 1,
			ContentHash: b2, BlobID: b2, Size: 1, Metadata: json.RawMessage(`{}`),
		}})
	}()
	go func() {
		defer wg.Done()
		r2, e2 = e.eng.Push(e.ctx, dev2, key2, []syncengine.ChangeInput{{
			ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpUpdate, BaseRevision: 1,
			ContentHash: b3, BlobID: b3, Size: 1, Metadata: json.RawMessage(`{}`),
		}})
	}()
	wg.Wait()
	if e1 != nil || e2 != nil {
		t.Fatalf("%v %v", e1, e2)
	}
	ok, conflict := 0, 0
	for _, r := range []syncengine.PushResult{r1[0], r2[0]} {
		switch r.Status {
		case "ok":
			ok++
		case "conflict":
			conflict++
		default:
			t.Fatalf("%+v", r)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestDuplicateDeliveryIdempotent(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	b := put(t, e, []byte("x"))
	key := ids.New()
	item := syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`),
	}
	r1 := push(t, e, key, item)
	r2 := push(t, e, key, item)
	if r1[0].Seq != r2[0].Seq || r1[0].Revision != r2[0].Revision {
		t.Fatalf("%+v vs %+v", r1[0], r2[0])
	}
	ch, _ := e.eng.GetChanges(e.ctx, e.dev, 0, 50, "")
	if len(ch.Changes) != 1 {
		t.Fatalf("journal grew: %d", len(ch.Changes))
	}
}

func TestGetChangesReplay(t *testing.T) {
	e := setup(t)
	b := put(t, e, []byte("z"))
	push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: ids.New(), CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`),
	})
	a, _ := e.eng.GetChanges(e.ctx, e.dev, 0, 10, "")
	c, _ := e.eng.GetChanges(e.ctx, e.dev, 0, 10, "")
	if len(a.Changes) != len(c.Changes) || a.Changes[0].Seq != c.Changes[0].Seq {
		t.Fatal("GET_CHANGES must be repeatable before ACK")
	}
	if err := e.eng.Ack(e.ctx, e.dev.ID, a.NextSeq); err != nil {
		t.Fatal(err)
	}
	// ACK does not hide journal; client still can fetch if it wants
	d, _ := e.eng.GetChanges(e.ctx, e.dev, a.NextSeq, 10, "")
	if len(d.Changes) != 0 {
		t.Fatalf("unexpected %v", d.Changes)
	}
}

func TestStaleAndFutureCursor(t *testing.T) {
	e := setup(t)
	h, err := e.eng.Hello(e.ctx, 99, false)
	if err != nil {
		t.Fatal(err)
	}
	if !h.NeedReconcile {
		t.Fatal("cursor ahead should reconcile")
	}
	h, _ = e.eng.Hello(e.ctx, 0, true)
	if !h.NeedReconcile {
		t.Fatal("restore hint")
	}
	if err := e.eng.Ack(e.ctx, e.dev.ID, 5); err == nil {
		t.Fatal("ack past head")
	}
}

func TestPartialBatch(t *testing.T) {
	e := setup(t)
	b := put(t, e, []byte("p"))
	good := ids.New()
	res := push(t, e, ids.New(),
		syncengine.ChangeInput{ObjectID: good, CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`)},
		syncengine.ChangeInput{ObjectID: ids.New(), CollectionID: e.col.ID, Op: syncengine.OpUpdate, BaseRevision: 3, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`)},
	)
	if res[0].Status != "ok" {
		t.Fatalf("%+v", res[0])
	}
	if res[1].Status == "ok" {
		t.Fatalf("second should fail: %+v", res[1])
	}
	ch, _ := e.eng.GetChanges(e.ctx, e.dev, 0, 10, "")
	if len(ch.Changes) != 1 {
		t.Fatalf("only first should commit, got %d", len(ch.Changes))
	}
}

func TestConflictOnStaleBaseRevision(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	b := put(t, e, []byte("1"))
	push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`)})
	b2 := put(t, e, []byte("2"))
	push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpUpdate, BaseRevision: 1, ContentHash: b2, BlobID: b2, Size: 1, Metadata: json.RawMessage(`{}`)})
	b3 := put(t, e, []byte("3"))
	res := push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpUpdate, BaseRevision: 1, ContentHash: b3, BlobID: b3, Size: 1, Metadata: json.RawMessage(`{}`)})
	if res[0].Status != "conflict" || res[0].ServerRevision != 2 {
		t.Fatalf("%+v", res[0])
	}
}

func TestReconcile(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	b := put(t, e, []byte("q"))
	push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`)})
	ghost := ids.New()
	res, err := e.eng.Reconcile(e.ctx, e.dev, e.col.ID, []syncengine.InventoryItem{
		{ID: ghost, Revision: 1, ContentHash: b},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingOnClient) != 1 || res.MissingOnClient[0] != oid {
		t.Fatalf("missing on client %+v", res)
	}
	if len(res.MissingOnServer) != 1 || res.MissingOnServer[0] != ghost {
		t.Fatalf("missing on server %+v", res)
	}
}

func TestAckDoesNotMoveBackwards(t *testing.T) {
	e := setup(t)
	b := put(t, e, []byte("n"))
	push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: ids.New(), CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 1, Metadata: json.RawMessage(`{}`)})
	ch, _ := e.eng.GetChanges(e.ctx, e.dev, 0, 10, "")
	if err := e.eng.Ack(e.ctx, e.dev.ID, ch.NextSeq); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.Ack(e.ctx, e.dev.ID, 0); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataOnlyCreate(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	res := push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Kind: "directory", Op: syncengine.OpCreate,
		Metadata: json.RawMessage(`{"name":"dir"}`),
	})
	if res[0].Status != "ok" {
		t.Fatalf("%+v", res[0])
	}
}

func TestReopenAfterPush(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	b := put(t, e, []byte("persist"))
	push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, ContentHash: b, BlobID: b, Size: 8, Metadata: json.RawMessage(`{"name":"p.txt"}`),
	})
	dir := e.st.Dir
	blobDir := e.st.BlobDir
	tmpDir := e.st.TmpDir
	key := append([]byte(nil), e.st.BlobKey...)
	dbPath := filepath.Join(dir, "m.db")
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir, dbPath, blobDir, tmpDir, key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := syncengine.New(st)
	obj, err := eng.GetObject(e.ctx, oid)
	if err != nil {
		t.Fatal(err)
	}
	if obj.BlobID != b {
		t.Fatalf("%+v", obj)
	}
}

func TestExtraMetadataBlobRefs(t *testing.T) {
	e := setup(t)
	orig := put(t, e, []byte("orig-bytes"))
	prev := put(t, e, []byte("preview-bytes"))
	meta, _ := json.Marshal(map[string]string{"preview_hash": prev, "name": "x.jpg"})
	oid := ids.New()
	r := push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Kind: "photo", Op: syncengine.OpCreate,
		ContentHash: orig, BlobID: orig, Size: 10, Metadata: meta,
	})
	if r[0].Status != "ok" {
		t.Fatalf("%+v", r[0])
	}
	var rc int
	if err := e.st.DB.QueryRow(`SELECT refcount FROM blobs WHERE id=?`, prev).Scan(&rc); err != nil || rc != 1 {
		t.Fatalf("preview refcount %d %v", rc, err)
	}
	push(t, e, ids.New(), syncengine.ChangeInput{
		ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpDelete, BaseRevision: 1,
	})
	if err := e.st.DB.QueryRow(`SELECT refcount FROM blobs WHERE id=?`, prev).Scan(&rc); err != nil || rc != 0 {
		t.Fatalf("preview after delete %d %v", rc, err)
	}
}

func TestCreateConflict(t *testing.T) {
	e := setup(t)
	oid := ids.New()
	push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, Metadata: json.RawMessage(`{}`)})
	res := push(t, e, ids.New(), syncengine.ChangeInput{ObjectID: oid, CollectionID: e.col.ID, Op: syncengine.OpCreate, Metadata: json.RawMessage(`{}`)})
	if res[0].Status != "conflict" {
		t.Fatalf("%+v", res[0])
	}
}

func TestCollectionMetadataMerge(t *testing.T) {
	e := setup(t)
	if err := e.eng.SetCollectionMetadata(e.ctx, e.col.ID, json.RawMessage(`{"keep":true}`)); err != nil {
		t.Fatal(err)
	}
	got, err := e.eng.PatchCollectionMetadata(e.ctx, e.col.ID, json.RawMessage(`{"color":"#FF0000"}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(got.Metadata, &m); err != nil {
		t.Fatal(err)
	}
	if m["keep"] != true || m["color"] != "#FF0000" {
		t.Fatalf("%s", got.Metadata)
	}
	again, err := e.eng.PatchCollectionMetadata(e.ctx, e.col.ID, json.RawMessage(`{"color":"#FF0000"}`))
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != got.Revision {
		t.Fatalf("unchanged color bumped revision %d -> %d", got.Revision, again.Revision)
	}
}
