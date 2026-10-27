package agent_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nubilo/internal/agent"
	"nubilo/internal/dav"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/syncengine"
)

// fakeCal is a read-only Calendar app.
type fakeCal struct {
	mu        sync.Mutex
	calendars []agent.CalendarInfo
	events    map[string][]agent.LocalEvent
	listErr   error
}

func (f *fakeCal) ListCalendars() ([]agent.CalendarInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.CalendarInfo(nil), f.calendars...), nil
}

func (f *fakeCal) ListEvents(calendarID string, start, end time.Time) ([]agent.LocalEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]agent.LocalEvent(nil), f.events[calendarID]...), nil
}

func (f *fakeCal) set(calendarID string, evs ...agent.LocalEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[calendarID] = evs
}

func testEvent(t *testing.T, calendarID, uid, summary string, start time.Time) agent.LocalEvent {
	t.Helper()
	ics, err := agent.EncodeEventICS(agent.EventSpec{UID: uid, Summary: summary, Start: start, End: start.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return agent.LocalEvent{ID: "ek-" + uid, CalendarID: calendarID, UID: uid, ICS: ics, StartMS: start.UnixMilli()}
}

func mirrorAgent(h *harness, cal *fakeCal, sels ...agent.CalendarSel) *agent.Agent {
	sel := agent.DefaultSelection()
	sel.Calendars = sels
	a := newAgent(h, sel)
	a.Cal = cal
	return a
}

func TestMirrorCalendar(t *testing.T) {
	h := startHarness(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "ex-1", Title: "Work", Color: "#0E61B9"}},
		events:    map[string][]agent.LocalEvent{},
	}
	cal.set("ex-1", testEvent(t, "ex-1", "standup", "Standup", now.Add(time.Hour)), testEvent(t, "ex-1", "review", "Review", now.Add(48*time.Hour)))
	a := mirrorAgent(h, cal, agent.CalendarSel{LocalID: "ex-1", Title: "Work"})
	if err := a.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	col := collection(t, h.eng, "calendar", "Work")
	meta := dav.ParseCalendarColMeta(col.Metadata)
	if !meta.Mirror || meta.Color != "#0E61B9" {
		t.Fatalf("metadata %+v", meta)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 2 {
		t.Fatalf("mirrored events %d, want 2", n)
	}

	// A phone (app device) cannot change the copy.
	phone := &identity.Device{ID: "iphone", Role: identity.RoleClient, UserID: h.dev.UserID,
		Permissions: identity.DefaultPermissions(identity.RoleClient)}
	objs, _ := h.eng.ListObjects(ctx, col.ID)
	res, err := h.eng.Push(ctx, phone, ids.New(), []syncengine.ChangeInput{{
		ObjectID: objs[0].ID, CollectionID: col.ID, Op: syncengine.OpDelete, BaseRevision: objs[0].Revision, Force: true,
	}})
	if err != nil || res[0].Error != syncengine.ErrMirrorReadOnly {
		t.Fatalf("phone delete: %v %+v", err, res)
	}

	// An empty listing (access revoked, account loading) keeps the copy.
	cal.set("ex-1")
	if err := a.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 2 {
		t.Fatalf("empty listing deleted events: %d left", n)
	}
	// So does a failed one.
	cal.listErr = errors.New("eventkit: access denied")
	if err := a.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, h.eng, "calendar", "Work"); n != 2 {
		t.Fatalf("failed listing deleted events: %d left", n)
	}
	cal.listErr = nil

	// An event deleted on the Mac goes; an edit is copied.
	cal.set("ex-1", testEvent(t, "ex-1", "standup", "Standup moved", now.Add(2*time.Hour)))
	if err := a.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	objs, _ = h.eng.ListObjects(ctx, col.ID)
	if len(objs) != 1 {
		t.Fatalf("after Mac delete %d events", len(objs))
	}
	pt, err := h.st.GetBlobPlaintext(objs[0].BlobID)
	if err != nil || !bytes.Contains(pt, []byte("Standup moved")) {
		t.Fatalf("edit not mirrored: %v %s", err, pt)
	}
}

func TestMirrorNeverTakesOverOwnCalendar(t *testing.T) {
	h := startHarness(t)
	ctx := context.Background()
	own, err := h.eng.EnsureNamedCollection(ctx, h.dev.UserID, "calendar", "Calendar")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	cal := &fakeCal{
		calendars: []agent.CalendarInfo{{ID: "ex", Title: "Calendar"}, {ID: "icloud", Title: "Calendar"}},
		events:    map[string][]agent.LocalEvent{},
	}
	cal.set("ex", testEvent(t, "ex", "a", "Exchange meeting", now))
	cal.set("icloud", testEvent(t, "icloud", "b", "Dentist", now))
	a := mirrorAgent(h, cal, agent.CalendarSel{LocalID: "ex", Title: "Calendar"}, agent.CalendarSel{LocalID: "icloud", Title: "Calendar"})
	for i := 0; i < 2; i++ { // a second pass must keep the same names
		if err := a.SyncOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := liveCount(t, h.eng, "calendar", "Calendar"); n != 0 {
		t.Fatalf("own calendar got %d mirrored events", n)
	}
	if got, _ := h.eng.GetCollection(ctx, own.ID); dav.ParseCalendarColMeta(got.Metadata).Mirror {
		t.Fatal("own calendar marked as mirror")
	}
	for _, name := range []string{"Calendar (Mac)", "Calendar (Mac 2)"} {
		if n := liveCount(t, h.eng, "calendar", name); n != 1 {
			t.Fatalf("%s has %d events, want 1", name, n)
		}
	}
}
