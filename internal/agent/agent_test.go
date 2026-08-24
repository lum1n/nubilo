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
	"strings"
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

type fakeCal struct {
	mu        sync.Mutex
	calendars []agent.CalendarInfo
	events    map[string][]agent.LocalEvent
	listErr   error
	seq       int
}

func (f *fakeCal) ListCalendars() ([]agent.CalendarInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]agent.CalendarInfo(nil), f.calendars...)
	return out, nil
}

func (f *fakeCal) ListEvents(calendarID string, start, end time.Time) ([]agent.LocalEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []agent.LocalEvent
	for _, ev := range f.events[calendarID] {
		t := time.UnixMilli(ev.StartMS).UTC()
		if t.Before(start.UTC()) || t.After(end.UTC()) {
			continue
		}
		cp := ev
		cp.ICS = append([]byte(nil), ev.ICS...)
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakeCal) UpsertEvent(calendarID, localID string, ics []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if localID == "" {
		f.seq++
		localID = "ek-" + ids.New()[:8]
	}
	ev := agent.LocalEvent{
		ID: localID, CalendarID: calendarID, UID: agent.UIDFromICS(ics),
		ICS: append([]byte(nil), ics...), StartMS: agent.EventStartMS(ics),
	}
	list := f.events[calendarID]
	for i := range list {
		if list[i].ID == localID {
			list[i] = ev
			f.events[calendarID] = list
			return localID, nil
		}
	}
	f.events[calendarID] = append(list, ev)
	return localID, nil
}

func (f *fakeCal) DeleteEvent(localID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for cal, list := range f.events {
		out := list[:0]
		for _, ev := range list {
			if ev.ID != localID {
				out = append(out, ev)
			}
		}
		f.events[cal] = out
	}
	return nil
}

func (f *fakeCal) eventICS(calendarID, localID string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events[calendarID] {
		if ev.ID == localID {
			return append([]byte(nil), ev.ICS...)
		}
	}
	return nil
}

type fakeBook struct {
	mu      sync.Mutex
	list    []agent.LocalContact
	listErr error
}

func (f *fakeBook) ListContacts() ([]agent.LocalContact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]agent.LocalContact, len(f.list))
	copy(out, f.list)
	for i := range out {
		out[i].VCard = append([]byte(nil), f.list[i].VCard...)
	}
	return out, nil
}

func (f *fakeBook) UpsertContact(localID string, vcf []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if localID == "" {
		localID = "cn-" + ids.New()[:8]
	}
	c := agent.LocalContact{ID: localID, UID: agent.UIDFromVCard(vcf), VCard: append([]byte(nil), vcf...)}
	for i := range f.list {
		if f.list[i].ID == localID {
			f.list[i] = c
			return localID, nil
		}
	}
	f.list = append(f.list, c)
	return localID, nil
}

func (f *fakeBook) DeleteContact(localID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.list[:0]
	for _, c := range f.list {
		if c.ID != localID {
			out = append(out, c)
		}
	}
	f.list = out
	return nil
}

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
	dev, err := idsvc.Enroll(context.Background(), "studio-mac", devPub, identity.RoleAgent)
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
		client: protocol.NewClient(ts.URL, dev.ID, devPriv, false),
		m:      mp,
	}
}

func testEvent(t *testing.T, uid, summary string, start time.Time) agent.LocalEvent {
	t.Helper()
	end := start.Add(time.Hour)
	ics, err := agent.EncodeICS(uid, summary, start, end)
	if err != nil {
		t.Fatal(err)
	}
	return agent.LocalEvent{
		ID: "local-" + uid, CalendarID: "cal-1", UID: uid, ICS: ics, StartMS: start.UnixMilli(),
	}
}

func testVCard(uid, fn string) []byte {
	return []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\nEND:VCARD\r\n")
}

func liveCount(t *testing.T, eng *syncengine.Engine, kind, name string) int {
	t.Helper()
	col, err := eng.FindChildCollection(context.Background(), kind, "", name)
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
	col, err := eng.FindChildCollection(context.Background(), kind, "", name)
	if err != nil {
		t.Fatal(err)
	}
	return col
}

func newAgent(h *harness, sel agent.Selection, cal agent.CalendarSource, book agent.ContactSource) *agent.Agent {
	return &agent.Agent{
		Client: h.client, Map: h.m, Sel: sel, Cal: cal, Contacts: book,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestPushLocalEvent(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-push", "Standup", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	if err := newAgent(h, sel, cal, nil).SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 1 {
		t.Fatalf("live objects %d", n)
	}
	col := collection(t, h.eng, "calendar", "Work")
	obj, err := h.eng.FindObjectByUID(context.Background(), col.ID, "uid-push")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := h.st.GetBlobPlaintext(obj.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pt), "Standup") {
		t.Fatalf("blob %s", pt)
	}
}

func TestFailedListDoesNotDelete(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-keep", "Keep me", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	a := newAgent(h, sel, cal, nil)
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cal.mu.Lock()
	cal.listErr = errors.New("eventkit timeout")
	cal.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 1 {
		t.Fatalf("failed list deleted objects, live=%d", n)
	}
}

func TestMissingInWindowDeletes(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-gone", "Soon gone", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	a := newAgent(h, sel, cal, nil)
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cal.mu.Lock()
	cal.events["cal-1"] = nil
	cal.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 0 {
		t.Fatalf("expected tombstone, live=%d", n)
	}
}

func TestOutOfWindowIsNotDelete(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().AddDate(0, 0, -10).Truncate(time.Second)
	ev := testEvent(t, "uid-old", "Old meeting", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	a := newAgent(h, sel, cal, nil)
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Sel.WindowDays = 1
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 1 {
		t.Fatalf("out-of-window treated as delete, live=%d", n)
	}
}

func TestRemoteChangeApplied(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-remote", "Original", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	a := newAgent(h, sel, cal, nil)
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	col := collection(t, h.eng, "calendar", "Work")
	obj, err := h.eng.FindObjectByUID(context.Background(), col.ID, "uid-remote")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := agent.EncodeICS("uid-remote", "Updated title", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	hash := ncrypto.SHA256Hex(updated)
	if _, _, err := h.st.PutBlob(context.Background(), strings.NewReader(string(updated)), hash); err != nil {
		t.Fatal(err)
	}
	res, err := h.eng.Push(context.Background(), h.dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: col.ID, Kind: "event", Op: syncengine.OpUpdate,
		BaseRevision: obj.Revision, ContentHash: hash, BlobID: hash, Size: int64(len(updated)),
		Metadata: dav.EncodeEventMeta(dav.EventMeta{Name: "uid-remote.ics", UID: "uid-remote", Comp: "VEVENT"}),
		Force:    true,
	}})
	if err != nil || len(res) == 0 || res[0].Status != "ok" {
		t.Fatalf("push update %+v %v", res, err)
	}
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := cal.eventICS("cal-1", ev.ID)
	if !strings.Contains(string(got), "Updated title") {
		t.Fatalf("local source not updated: %s", got)
	}
}

func TestPushContact(t *testing.T) {
	h := startHarness(t)
	book := &fakeBook{list: []agent.LocalContact{{
		ID: "cn-1", UID: "contact-1", VCard: testVCard("contact-1", "Ada Lovelace"),
	}}}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, SyncContacts: true}
	if err := newAgent(h, sel, nil, book).SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "addressbook", "Contacts"); n != 1 {
		t.Fatalf("live contacts %d", n)
	}
}

func TestFailedContactListDoesNotDelete(t *testing.T) {
	h := startHarness(t)
	book := &fakeBook{list: []agent.LocalContact{{
		ID: "cn-1", UID: "contact-keep", VCard: testVCard("contact-keep", "Grace Hopper"),
	}}}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, SyncContacts: true}
	a := newAgent(h, sel, nil, book)
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	book.mu.Lock()
	book.listErr = errors.New("contacts enumerate failed")
	book.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "addressbook", "Contacts"); n != 1 {
		t.Fatalf("failed list deleted contacts, live=%d", n)
	}
}

func TestOpenPlatformLinux(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("linux stub only")
	}
	_, _, _, err := agent.OpenPlatform(agent.DefaultSelection())
	if !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("got %v", err)
	}
	if _, err := agent.PlatformCalendars(); !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("calendars %v", err)
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
	s.SelectCalendar("abc", "Work")
	s.SyncContacts = true
	if err := agent.SaveSelection(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := agent.LoadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Calendars) != 1 || got.Calendars[0].LocalID != "abc" || !got.SyncContacts {
		t.Fatalf("%+v", got)
	}
	got.UnselectCalendar("abc")
	if len(got.Calendars) != 0 {
		t.Fatalf("unselect %+v", got.Calendars)
	}
}

func TestEncodeICSRoundTrip(t *testing.T) {
	start := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	ics, err := agent.EncodeICS("round-1", "Hello", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if agent.UIDFromICS(ics) != "round-1" {
		t.Fatalf("uid %s", agent.UIDFromICS(ics))
	}
	sum, st, en := agent.EventSummaryStartEnd(ics)
	if sum != "Hello" || !st.Equal(start) || !en.Equal(start.Add(time.Hour)) {
		t.Fatalf("summary=%s start=%s end=%s", sum, st, en)
	}
}

type fakePhotos struct {
	mu      sync.Mutex
	list    []agent.LocalPhoto
	listErr error
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
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel, nil, nil)
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
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel, nil, nil)
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
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel, nil, nil)
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
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel, nil, nil)
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
