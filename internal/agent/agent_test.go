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
	newIDs    bool
	upsertErr error
}

type fakeReminders struct {
	mu    sync.Mutex
	lists []agent.CalendarInfo
	todos map[string][]agent.LocalTodo
	seq   int
}

func (f *fakeReminders) ListReminderLists() ([]agent.CalendarInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.CalendarInfo(nil), f.lists...), nil
}

func (f *fakeReminders) ListReminders(listID string, start, end time.Time) ([]agent.LocalTodo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agent.LocalTodo
	for _, td := range f.todos[listID] {
		cp := td
		cp.ICS = append([]byte(nil), td.ICS...)
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakeReminders) UpsertReminder(listID, localID string, ics []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if localID == "" {
		f.seq++
		localID = "rem-" + ids.New()[:8]
	}
	uid := agent.UIDFromICS(ics)
	row := agent.LocalTodo{ID: localID, ListID: listID, UID: uid, ICS: append([]byte(nil), ics...), DueMS: agent.TodoDueMS(ics)}
	list := f.todos[listID]
	found := false
	for i := range list {
		if list[i].ID == localID {
			list[i] = row
			found = true
			break
		}
	}
	if !found {
		list = append(list, row)
	}
	if f.todos == nil {
		f.todos = map[string][]agent.LocalTodo{}
	}
	f.todos[listID] = list
	return localID, nil
}

func (f *fakeReminders) DeleteReminder(localID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for lid, list := range f.todos {
		out := list[:0]
		for _, td := range list {
			if td.ID != localID {
				out = append(out, td)
			}
		}
		f.todos[lid] = out
	}
	return nil
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
	if f.upsertErr != nil {
		return "", f.upsertErr
	}
	if localID == "" {
		f.seq++
		localID = "ek-" + ids.New()[:8]
	}
	prevID := localID
	if f.newIDs {
		localID = prevID + "-n"
	}
	ev := agent.LocalEvent{
		ID: localID, CalendarID: calendarID, UID: agent.UIDFromICS(ics),
		ICS: append([]byte(nil), ics...), StartMS: agent.EventStartMS(ics),
	}
	list := f.events[calendarID]
	for i := range list {
		if list[i].ID == prevID || list[i].ID == localID {
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
		client: protocol.NewClient(ts.URL, dev.ID, devPriv, protocol.TLS{}),
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

func TestPushCalendarColor(t *testing.T) {
	h := startHarness(t)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work", Color: "#0E61B9"}},
		events:    map[string][]agent.LocalEvent{},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	if err := newAgent(h, sel, cal, nil).SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	col := collection(t, h.eng, "calendar", "Work")
	if dav.ParseCalendarColMeta(col.Metadata).Color != "#0E61B9" {
		t.Fatalf("metadata %s", col.Metadata)
	}
}

func TestPushReminders(t *testing.T) {
	h := startHarness(t)
	due := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour)
	ics, err := agent.EncodeTodoICS(agent.TodoSpec{
		UID: "todo-push", Summary: "Buy milk", Due: due, Status: "NEEDS-ACTION",
	})
	if err != nil {
		t.Fatal(err)
	}
	rems := &fakeReminders{
		lists: []agent.CalendarInfo{{ID: "list-1", Title: "Errands", Color: "#FF2D55"}},
		todos: map[string][]agent.LocalTodo{
			"list-1": {{ID: "rem-1", ListID: "list-1", UID: "todo-push", ICS: ics, DueMS: due.UnixMilli()}},
		},
	}
	sel := agent.Selection{
		IntervalSeconds: 120, WindowDays: 730,
		Reminders: []agent.CalendarSel{{LocalID: "list-1", Title: "Errands"}},
	}
	a := newAgent(h, sel, nil, nil)
	a.Reminders = rems
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	col := collection(t, h.eng, "calendar", "Errands")
	meta := dav.ParseCalendarColMeta(col.Metadata)
	if meta.Comp != "VTODO" || meta.Color != "#FF2D55" {
		t.Fatalf("meta %+v %s", meta, col.Metadata)
	}
	if n := liveCount(t, h.eng, "calendar", "Errands"); n != 1 {
		t.Fatalf("live objects %d", n)
	}
	obj, err := h.eng.FindObjectByUID(context.Background(), col.ID, "todo-push")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := h.st.GetBlobPlaintext(obj.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pt), "BEGIN:VTODO") || !strings.Contains(string(pt), "Buy milk") {
		t.Fatalf("blob %s", pt)
	}
}

func TestReminderNameCollision(t *testing.T) {
	h := startHarness(t)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Personal"}},
		events:    map[string][]agent.LocalEvent{},
	}
	rems := &fakeReminders{
		lists: []agent.CalendarInfo{{ID: "list-1", Title: "Personal"}},
		todos: map[string][]agent.LocalTodo{},
	}
	sel := agent.Selection{
		IntervalSeconds: 120, WindowDays: 730,
		Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Personal"}},
		Reminders: []agent.CalendarSel{{LocalID: "list-1", Title: "Personal"}},
	}
	a := newAgent(h, sel, cal, nil)
	a.Reminders = rems
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.FindChildCollection(context.Background(), "calendar", "", "Personal"); err != nil {
		t.Fatal(err)
	}
	col, err := h.eng.FindChildCollection(context.Background(), "calendar", "", "Personal Reminders")
	if err != nil {
		t.Fatal(err)
	}
	if dav.ParseCalendarColMeta(col.Metadata).Comp != "VTODO" {
		t.Fatalf("%s", col.Metadata)
	}
}

func TestPushRecurringSeriesOneObject(t *testing.T) {
	h := startHarness(t)
	start := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	ics, err := agent.EncodeEventICS(agent.EventSpec{
		UID: "uid-series", Summary: "Weekly standup", Start: start, End: start.Add(time.Hour),
		RRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := agent.LocalEvent{ID: "series-1", CalendarID: "cal-1", UID: "uid-series", ICS: ics, StartMS: start.UnixMilli()}
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
	obj, err := h.eng.FindObjectByUID(context.Background(), col.ID, "uid-series")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := h.st.GetBlobPlaintext(obj.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pt), "RRULE") || !strings.Contains(string(pt), "FREQ=WEEKLY") {
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

func TestApplyChangeRebindsEventKitID(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-rebind", "Original", start)
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
	obj, err := h.eng.FindObjectByUID(context.Background(), col.ID, "uid-rebind")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := agent.EncodeICS("uid-rebind", "Rebound", start, start.Add(time.Hour))
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
		Metadata: dav.EncodeEventMeta(dav.EventMeta{Name: "uid-rebind.ics", UID: "uid-rebind", Comp: "VEVENT"}),
		Force:    true,
	}})
	if err != nil || len(res) == 0 || res[0].Status != "ok" {
		t.Fatalf("push update %+v %v", res, err)
	}
	cal.mu.Lock()
	cal.newIDs = true
	cal.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := a.Map.ByObject(obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalID == ev.ID {
		t.Fatal("expected rebind to new EventKit id")
	}
	rows, err := a.Map.ForCollection(col.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.ObjectID == obj.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("object mapped %d times", n)
	}
}

func TestApplyChangeFailureDoesNotAck(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-nack", "Original", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Calendars: []agent.CalendarSel{{LocalID: "cal-1", Title: "Work"}}}
	a := newAgent(h, sel, cal, nil)
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur := a.Map.Cursor()
	col := collection(t, h.eng, "calendar", "Work")
	obj, err := h.eng.FindObjectByUID(context.Background(), col.ID, "uid-nack")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := agent.EncodeICS("uid-nack", "Will fail", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	hash := ncrypto.SHA256Hex(updated)
	if _, _, err := h.st.PutBlob(context.Background(), strings.NewReader(string(updated)), hash); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Push(context.Background(), h.dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: col.ID, Kind: "event", Op: syncengine.OpUpdate,
		BaseRevision: obj.Revision, ContentHash: hash, BlobID: hash, Size: int64(len(updated)),
		Metadata: dav.EncodeEventMeta(dav.EventMeta{Name: "uid-nack.ics", UID: "uid-nack", Comp: "VEVENT"}),
		Force:    true,
	}}); err != nil {
		t.Fatal(err)
	}
	cal.mu.Lock()
	cal.upsertErr = errors.New("repeat field cannot be changed")
	cal.mu.Unlock()
	if err := a.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected apply failure")
	}
	if a.Map.Cursor() != cur {
		t.Fatalf("cursor advanced %d -> %d", cur, a.Map.Cursor())
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
	_, _, _, _, err := agent.OpenPlatform(agent.DefaultSelection())
	if !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("got %v", err)
	}
	if _, err := agent.PlatformCalendars(); !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("calendars %v", err)
	}
	if _, err := agent.PlatformReminderLists(); !errors.Is(err, agent.ErrNeedDarwin) {
		t.Fatalf("reminders %v", err)
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

func TestSelectionReloadPushesCalendar(t *testing.T) {
	h := startHarness(t)
	start := time.Now().UTC().Truncate(time.Second)
	ev := testEvent(t, "uid-reload", "After select", start)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "cal-1", Title: "Work"}},
		events:    map[string][]agent.LocalEvent{"cal-1": {ev}},
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	sel := agent.DefaultSelection()
	if err := agent.SaveSelection(path, sel); err != nil {
		t.Fatal(err)
	}
	a := newAgent(h, sel, cal, nil)
	a.SelPath = path
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 0 {
		t.Fatalf("pushed before select: %d", n)
	}
	sel.SelectCalendar("cal-1", "Work")
	if err := agent.SaveSelection(path, sel); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 1 {
		t.Fatalf("after reload live objects %d", n)
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

func TestPushPhotoModChangeRepushes(t *testing.T) {
	h := startHarness(t)
	orig1 := testJPEGBytes(t, 40, 40)
	orig2 := testJPEGBytes(t, 48, 48)
	src := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "pk-mod", Filename: "m.jpg", TakenAtMS: time.Now().UnixMilli(), ModMS: 1000, Original: orig1,
	}}}
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel, nil, nil)
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
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	a := newAgent(h, sel, nil, nil)
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
	sel := agent.Selection{IntervalSeconds: 120, WindowDays: 730, Photos: agent.PhotosSel{Enabled: true, Source: "all"}}
	// Separate idmap so this agent does not already know the object a2 pushed.
	mp, err := agent.OpenMap(filepath.Join(t.TempDir(), "agent-b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mp.Close() })
	a := newAgent(h, sel, nil, nil)
	a.Map = mp
	a.Photos = src
	src2 := &fakePhotos{list: []agent.LocalPhoto{{
		ID: "remote-1", Filename: "r.jpg", TakenAtMS: time.Now().UnixMilli(), ModMS: 5, Original: orig,
	}}}
	a2 := newAgent(h, sel, nil, nil)
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
