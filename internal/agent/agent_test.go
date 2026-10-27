package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nubilo/internal/agent"
	"nubilo/internal/audit"
	"nubilo/internal/auth"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/protocol"
	"nubilo/internal/server"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

type harness struct {
	ts     *httptest.Server
	eng    *syncengine.Engine
	st     *store.Store
	dev    *identity.Device
	client *protocol.Client
	m      *agent.Map
}

func startHarness(t *testing.T) *harness {
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
	a := &auth.Authenticator{IDs: idsvc, Store: st, SkewMS: 60_000, AdminTok: []byte("admintok")}
	cfg := config.Defaults(dir)
	eng := syncengine.New(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, st, idsvc, a, eng, &audit.Logger{Store: st, Slog: log}, log, pub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	devPub, devPriv, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	dev, err := idsvc.Enroll(context.Background(), "", "studio-mac", devPub, identity.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := agent.OpenMap(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mp.Close() })
	return &harness{
		ts: ts, eng: eng, st: st, dev: dev,
		client: protocol.NewClient(ts.URL, dev.ID, devPriv, protocol.TLS{}),
		m:      mp,
	}
}

func liveCount(t *testing.T, eng *syncengine.Engine, kind, name string) int {
	t.Helper()
	col, err := eng.FindChildCollection(context.Background(), "", kind, "", name)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return 0
		}
		t.Fatal(err)
	}
	objs, err := eng.ListObjects(context.Background(), col.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(objs)
}

func collection(t *testing.T, eng *syncengine.Engine, kind, name string) *syncengine.Collection {
	t.Helper()
	col, err := eng.FindChildCollection(context.Background(), "", kind, "", name)
	if err != nil {
		t.Fatal(err)
	}
	return col
}

func newAgent(h *harness, sel agent.Selection) *agent.Agent {
	return &agent.Agent{
		Client: h.client, Map: h.m, Sel: sel,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// failingFiles is the real folder source with writes that can be made to fail.
type failingFiles struct {
	agent.FileSource
	writeErr error
}

func (f *failingFiles) WriteFile(absPath string, data []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	return f.FileSource.WriteFile(absPath, data)
}

func filesSel(root string) agent.Selection {
	return agent.Selection{
		IntervalSeconds: 120,
		Files: agent.FilesSel{
			Enabled: true,
			Folders: []agent.FileFolderSel{{Path: root, Name: "SyncRoot"}},
		},
	}
}

func TestApplyChangeFailureDoesNotAck(t *testing.T) {
	h := startHarness(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &failingFiles{FileSource: agent.OpenFiles()}
	a := newAgent(h, filesSel(root))
	a.Files = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur := a.Map.Cursor()
	col := collection(t, h.eng, "files", "SyncRoot")
	objs, err := h.eng.ListObjects(context.Background(), col.ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("objects %d %v", len(objs), err)
	}
	obj := objs[0]
	updated := []byte("changed on the server")
	hash := ncrypto.SHA256Hex(updated)
	if _, _, err := h.st.PutUserBlob(context.Background(), h.dev.UserID, bytes.NewReader(updated), hash); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Push(context.Background(), h.dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: col.ID, Kind: "file", Op: syncengine.OpUpdate,
		BaseRevision: obj.Revision, ContentHash: hash, BlobID: hash, Size: int64(len(updated)),
		Metadata: dav.EncodeFileMeta(dav.FileMeta{Name: "note.txt", MIME: "text/plain"}),
		Force:    true,
	}}); err != nil {
		t.Fatal(err)
	}
	src.writeErr = errors.New("disk full")
	if err := a.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected apply failure")
	}
	if a.Map.Cursor() != cur {
		t.Fatalf("cursor advanced %d -> %d", cur, a.Map.Cursor())
	}
}

func TestLoadPairedClient(t *testing.T) {
	dir := t.TempDir()
	if _, err := agent.LoadPairedClient(dir, false); !errors.Is(err, agent.ErrNotPaired) {
		t.Fatalf("unpaired %v", err)
	}
	pub, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"dev1","server":"http://127.0.0.1:9","name":"mac"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ncrypto.WriteKeyFile(filepath.Join(dir, "device.key"), ncrypto.PrivateKeyBytes(priv)); err != nil {
		t.Fatal(err)
	}
	c, err := agent.LoadPairedClient(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.DeviceID != "dev1" {
		t.Fatalf("device %s", c.DeviceID)
	}
}

func TestSelectionPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	s := agent.DefaultSelection()
	s.Files.Enabled = true
	s.AddFileFolder("/tmp/docs", "Docs")
	if err := agent.SaveSelection(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := agent.LoadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Files.Enabled || len(got.Files.Folders) != 1 || got.Files.Folders[0].Name != "Docs" {
		t.Fatalf("%+v", got)
	}
	got.RemoveFileFolder("/tmp/docs")
	if len(got.Files.Folders) != 0 {
		t.Fatalf("unselect %+v", got)
	}
}

func TestSelectionIgnoresRemovedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	old := `{"interval_seconds":60,"window_days":730,"calendars":[{"local_id":"a","title":"Work"}],"sync_contacts":true,"photos":{"enabled":true,"source":"all","albums":["x"]},"files":{"enabled":true}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := agent.LoadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.IntervalSeconds != 60 || !got.Files.Enabled {
		t.Fatalf("%+v", got)
	}
}

func TestSelectionReloadPushesFiles(t *testing.T) {
	h := startHarness(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	sel := agent.DefaultSelection()
	if err := agent.SaveSelection(path, sel); err != nil {
		t.Fatal(err)
	}
	a := newAgent(h, sel)
	a.Files = agent.OpenFiles()
	a.SelPath = path
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "files", "SyncRoot"); n != 0 {
		t.Fatalf("pushed before select: %d", n)
	}
	if err := agent.SaveSelection(path, filesSel(root)); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "files", "SyncRoot"); n != 1 {
		t.Fatalf("after reload live objects %d", n)
	}
}

func TestNestedFilesPush(t *testing.T) {
	h := startHarness(t)
	root := t.TempDir()
	sub := filepath.Join(root, "docs", "nested")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("top"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "mid.txt"), []byte("mid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("deep"), 0o600); err != nil {
		t.Fatal(err)
	}
	sel := agent.Selection{
		IntervalSeconds: 120,
		Files: agent.FilesSel{
			Enabled: true,
			Folders: []agent.FileFolderSel{{Path: root, Name: "SyncRoot"}},
		},
	}
	a := newAgent(h, sel)
	a.Files = agent.OpenFiles()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rootCol := collection(t, h.eng, "files", "SyncRoot")
	rootObjs, err := h.eng.ListObjects(context.Background(), rootCol.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootObjs) != 1 {
		t.Fatalf("root objects %d want 1 (top.txt only)", len(rootObjs))
	}
	docs, err := h.eng.FindChildCollection(context.Background(), "", "files", rootCol.ID, "docs")
	if err != nil {
		t.Fatalf("docs child: %v", err)
	}
	docsObjs, err := h.eng.ListObjects(context.Background(), docs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(docsObjs) != 1 {
		t.Fatalf("docs objects %d want 1", len(docsObjs))
	}
	nested, err := h.eng.FindChildCollection(context.Background(), "", "files", docs.ID, "nested")
	if err != nil {
		t.Fatalf("nested child: %v", err)
	}
	nestedObjs, err := h.eng.ListObjects(context.Background(), nested.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nestedObjs) != 1 {
		t.Fatalf("nested objects %d want 1", len(nestedObjs))
	}
}

func TestCursorPastServerHeadResets(t *testing.T) {
	h := startHarness(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAgent(h, filesSel(root))
	a.Files = agent.OpenFiles()
	// A cursor beyond the journal, as after restoring an older server backup.
	if err := a.Map.SetCursor(1_000_000); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	head, err := h.eng.HeadSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c := a.Map.Cursor(); c > head {
		t.Fatalf("cursor %d still past head %d", c, head)
	}
}
