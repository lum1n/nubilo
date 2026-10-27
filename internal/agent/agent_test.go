package agent_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
	"nubilo/internal/photos"
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

func TestOpenPlatformLinux(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("linux stub only")
	}
	if _, err := agent.OpenPlatform(); !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("got %v", err)
	}
	if _, err := agent.PlatformAlbums(); !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("albums %v", err)
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
	s.Photos.Enabled = true
	s.SelectAlbum("album-1")
	s.AddFileFolder("/tmp/docs", "Docs")
	if err := agent.SaveSelection(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := agent.LoadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Photos.Enabled || len(got.Photos.Albums) != 1 || len(got.Files.Folders) != 1 || got.Files.Folders[0].Name != "Docs" {
		t.Fatalf("%+v", got)
	}
	got.UnselectAlbum("album-1")
	got.RemoveFileFolder("/tmp/docs")
	if len(got.Photos.Albums) != 0 || len(got.Files.Folders) != 0 {
		t.Fatalf("unselect %+v", got)
	}
}

func TestSelectionIgnoresRemovedCalendarFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	old := `{"interval_seconds":60,"window_days":730,"calendars":[{"local_id":"a","title":"Work"}],"sync_contacts":true,"photos":{"enabled":true}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := agent.LoadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.IntervalSeconds != 60 || !got.Photos.Enabled {
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

type fakePhotos struct {
	mu       sync.Mutex
	list     []agent.LocalPhoto
	listErr  error
	imported []string
}

func (f *fakePhotos) ListAlbums() ([]agent.PhotoInfo, error) {
	return nil, nil
}

func (f *fakePhotos) ListPhotos(filter agent.PhotoFilter) ([]agent.LocalPhoto, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []agent.LocalPhoto
	for _, p := range f.list {
		if filter.AfterMS > 0 && p.TakenAtMS < filter.AfterMS {
			continue
		}
		if filter.BeforeMS > 0 && p.TakenAtMS > filter.BeforeMS {
			continue
		}
		cp := p
		cp.Original = append([]byte(nil), p.Original...)
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakePhotos) ReadOriginal(id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.list {
		if p.ID == id {
			return append([]byte(nil), p.Original...), nil
		}
	}
	return nil, errors.New("missing")
}

func (f *fakePhotos) ReadLiveMovie(id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.list {
		if p.ID == id {
			if len(p.LiveMovie) == 0 {
				return nil, nil
			}
			return append([]byte(nil), p.LiveMovie...), nil
		}
	}
	return nil, nil
}

func (f *fakePhotos) ImportOriginal(data []byte, filename, albumID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "imported-" + filename
	f.list = append(f.list, agent.LocalPhoto{
		ID: id, Filename: filename, Original: append([]byte(nil), data...),
		ModMS: time.Now().UnixMilli(), Kind: "image",
	})
	f.imported = append(f.imported, id)
	return id, nil
}

func testJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPushPhoto(t *testing.T) {
	h := startHarness(t)
	orig := testJPEGBytes(t, 64, 48)
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-1", Filename: "a.jpg", TakenAtMS: time.Now().UnixMilli(), Original: orig,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel)
	a.Photos = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "photos", "Photos"); n != 1 {
		t.Fatalf("live photos %d", n)
	}
	col := collection(t, h.eng, "photos", "Photos")
	objs, err := h.eng.ListObjects(context.Background(), col.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := photos.ParseMeta(objs[0].Metadata)
	if photos.HasLatLon(objs[0].Metadata) {
		t.Fatal("gps in metadata")
	}
	pt, err := h.st.GetBlobPlaintext(objs[0].BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, orig) {
		t.Fatal("original not preserved")
	}
	if m.ThumbHash == "" || m.PreviewHash == "" {
		t.Fatal("missing derivatives")
	}
}

func TestFailedPhotoListDoesNotDelete(t *testing.T) {
	h := startHarness(t)
	orig := testJPEGBytes(t, 32, 32)
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-keep", Filename: "k.jpg", TakenAtMS: time.Now().UnixMilli(), Original: orig,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel)
	a.Photos = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	src.listErr = errors.New("photokit timeout")
	src.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "photos", "Photos"); n != 1 {
		t.Fatalf("failed list deleted photos, live=%d", n)
	}
}

func TestMissingPhotoDeletes(t *testing.T) {
	h := startHarness(t)
	orig := testJPEGBytes(t, 32, 32)
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-gone", Filename: "g.jpg", TakenAtMS: time.Now().UnixMilli(), Original: orig,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel)
	a.Photos = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	src.list = nil
	src.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "photos", "Photos"); n != 0 {
		t.Fatalf("expected tombstone, live=%d", n)
	}
}

func TestPhotoDateRangeIsNotDelete(t *testing.T) {
	h := startHarness(t)
	orig := testJPEGBytes(t, 32, 32)
	old := time.Now().AddDate(0, 0, -10).UnixMilli()
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-old", Filename: "old.jpg", TakenAtMS: old, Original: orig,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel)
	a.Photos = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Sel.Photos.Source = "dates"
	a.Sel.Photos.AfterMS = time.Now().AddDate(0, 0, -1).UnixMilli()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "photos", "Photos"); n != 1 {
		t.Fatalf("out-of-range treated as delete, live=%d", n)
	}
}

func TestPushPhotoModChangeRepushes(t *testing.T) {
	h := startHarness(t)
	orig1 := testJPEGBytes(t, 40, 40)
	orig2 := testJPEGBytes(t, 48, 48)
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-mod", Filename: "m.jpg", TakenAtMS: time.Now().UnixMilli(), ModMS: 1000, Original: orig1,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel)
	a.Photos = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	col := collection(t, h.eng, "photos", "Photos")
	objs, err := h.eng.ListObjects(context.Background(), col.ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("objs %v %d", err, len(objs))
	}
	firstHash := objs[0].ContentHash
	src.mu.Lock()
	src.list[0].Original = orig2
	src.list[0].ModMS = 2000
	src.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	objs, err = h.eng.ListObjects(context.Background(), col.ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("after %v %d", err, len(objs))
	}
	if objs[0].ContentHash == firstHash {
		t.Fatal("expected content update after mod change")
	}
	row, err := a.Map.ByLocal("photo", "pk-mod")
	if err != nil {
		t.Fatal(err)
	}
	if row.ModMS != 2000 {
		t.Fatalf("mod_ms %d", row.ModMS)
	}
}

func TestPushVideoMeta(t *testing.T) {
	h := startHarness(t)
	// Minimal ftyp/isom box so DetectMIME returns video/mp4.
	vid := []byte{
		0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm',
	}
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-vid", Filename: "clip.mp4", Kind: "video", DurationMS: 3500,
		TakenAtMS: time.Now().UnixMilli(), ModMS: 1, Original: vid,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel)
	a.Photos = src
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	col := collection(t, h.eng, "photos", "Photos")
	objs, err := h.eng.ListObjects(context.Background(), col.ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("%v %d", err, len(objs))
	}
	m := photos.ParseMeta(objs[0].Metadata)
	if m.Kind != "video" {
		t.Fatalf("kind %q", m.Kind)
	}
	if m.DurationMS != 3500 {
		t.Fatalf("duration %d", m.DurationMS)
	}
	if m.ThumbHash != "" {
		t.Fatal("video should not have thumb")
	}
}

func TestPhotoWriteBackImport(t *testing.T) {
	h := startHarness(t)
	orig := testJPEGBytes(t, 24, 24)
	src := &fakePhotos{}
	sel := agent.Selection{IntervalSeconds: 120, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	// Separate idmap so this agent does not already know the object a2 pushed.
	mp, err := agent.OpenMap(filepath.Join(t.TempDir(), "agent-b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mp.Close() })
	a := newAgent(h, sel)
	a.Map = mp
	a.Photos = src
	src2 := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "remote-1", Filename: "r.jpg", TakenAtMS: time.Now().UnixMilli(), ModMS: 5, Original: orig,
	}}}
	a2 := newAgent(h, sel)
	a2.Photos = src2
	if err := a2.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	n := len(src.imported)
	src.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected import, got %d", n)
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
