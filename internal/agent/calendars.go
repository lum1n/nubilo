package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/ids"
	"nubilo/internal/syncengine"
)

// Calendar mirroring copies selected calendars of the Mac's Calendar app
// (for example an Exchange account that only this Mac can reach) one way
// into server calendars marked "mirror". Phones see them through their
// CalDAV account as read-only calendars; the server refuses their edits
// (syncengine.ErrMirrorReadOnly), and nothing is ever written back here.

const (
	kindEvent = "event"
	kindCal   = "calendar"
)

func (a *Agent) pushCalendars(ctx context.Context) error {
	if a.Cal == nil || len(a.Sel.Calendars) == 0 {
		return nil
	}
	window := a.Sel.WindowDays
	if window <= 0 {
		window = defaultWindowDays
	}
	now := time.Now()
	start, end := now.AddDate(0, 0, -window), now.AddDate(0, 0, window)
	colors := map[string]string{}
	if cals, err := a.Cal.ListCalendars(); err == nil {
		for _, c := range cals {
			colors[c.ID] = c.Color
		}
	}
	used := map[string]bool{} // two Mac calendars may share a title
	for _, sel := range a.Sel.Calendars {
		if err := ctx.Err(); err != nil {
			return err
		}
		col, err := a.ensureMirrorCollection(sel.Title, colors[sel.LocalID], used)
		if err != nil {
			return err
		}
		used[col.Name] = true
		events, err := a.Cal.ListEvents(sel.LocalID, start, end)
		if err != nil {
			// A failed listing must not look like "every event was deleted".
			a.Log.Warn("list_events_failed", "calendar", sel.Title, "err", err.Error())
			continue
		}
		seen := map[string]bool{}
		for _, ev := range events {
			seen[ev.ID] = true
			if err := a.pushEvent(col.ID, ev); err != nil {
				a.Log.Warn("push_event", "err", err.Error(), "local", ev.ID)
			}
		}
		mapped, err := a.Map.ForCollection(col.ID)
		if err != nil {
			return err
		}
		if len(events) == 0 && len(mapped) > 0 {
			// EventKit returns nothing while access is revoked or the account
			// is still loading; keep the copy instead of emptying it.
			a.Log.Warn("calendar_empty_listing", "calendar", sel.Title, "mirrored", len(mapped))
			continue
		}
		winStart, winEnd := start.UnixMilli(), end.UnixMilli()
		deleted := 0
		for _, row := range mapped {
			if seen[row.LocalID] || row.StartMS == 0 || row.StartMS < winStart || row.StartMS > winEnd {
				continue
			}
			if err := a.pushDelete(row); err != nil {
				a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
				continue
			}
			deleted++
		}
		a.Log.Info("mirror_calendar", "name", col.Name, "events", len(events), "deleted", deleted)
	}
	return nil
}

// pushEvent writes ev to the mirror; the Mac's version always wins.
func (a *Agent) pushEvent(collectionID string, ev LocalEvent) error {
	hash := ncrypto.SHA256Hex(ev.ICS)
	row, mapErr := a.Map.ByLocal(kindEvent, ev.ID)
	if mapErr != nil && !errors.Is(mapErr, sql.ErrNoRows) {
		return mapErr
	}
	known := mapErr == nil && row.CollectionID == collectionID
	if known && row.ContentHash == hash {
		return nil
	}
	uid := ev.UID
	if uid == "" {
		uid = UIDFromICS(ev.ICS)
	}
	if uid == "" {
		uid = ev.ID
	}
	name := dav.DAVResourceName(uid+".ics", ".ics")
	if known && row.Name != "" {
		name = row.Name
	}
	if err := a.putBlob(hash, ev.ICS); err != nil {
		return err
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindEvent,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         int64(len(ev.ICS)),
		Metadata:     dav.EncodeEventMeta(dav.EventMeta{Name: name, UID: uid, Comp: "VEVENT"}),
		Force:        true,
	}
	if known {
		in.ObjectID, in.Op, in.BaseRevision = row.ObjectID, syncengine.OpUpdate, row.Revision
	} else {
		in.ObjectID, in.Op = ids.New(), syncengine.OpCreate
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		msg := "rejected"
		if len(res) > 0 {
			msg = res[0].Status + " " + res[0].Error
		}
		return fmt.Errorf("push event: %s", msg)
	}
	startMS := ev.StartMS
	if startMS == 0 {
		startMS = EventStartMS(ev.ICS)
	}
	return a.Map.Put(Mapping{
		LocalID: ev.ID, Kind: kindEvent, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision, StartMS: startMS, UID: uid, Name: name,
	})
}

// ensureMirrorCollection finds or creates the server calendar for a Mac
// calendar titled title and marks it as a mirror with the Mac's color.
func (a *Agent) ensureMirrorCollection(title, color string, used map[string]bool) (*syncengine.Collection, error) {
	cols, err := a.collections()
	if err != nil {
		return nil, err
	}
	name := mirrorName(title, cols, used)
	color = dav.NormalizeCalendarColor(color)
	for i := range cols {
		c := &cols[i]
		if c.Kind != kindCal || c.Name != name || c.DeletedAt != nil || c.ParentID != "" || !ownCollection(c) {
			continue
		}
		meta := dav.ParseCalendarColMeta(c.Metadata)
		if meta.Mirror && (color == "" || meta.Color == color) {
			return c, nil
		}
		break
	}
	a.colCache = nil
	return a.Client.EnsureCollection(kindCal, name, dav.EncodeCalendarColMeta(dav.CalendarColMeta{Mirror: true, Color: color}))
}

// mirrorName is the server name for a mirrored calendar: its title, unless
// one of the user's own calendars already has that name. Mirroring must
// never take over a calendar the phones edit, so the copy of an Exchange
// "Calendar" then becomes "Calendar (Mac)". Names in used belong to other
// calendars mirrored in the same pass.
func mirrorName(title string, cols []syncengine.Collection, used map[string]bool) string {
	if title == "" {
		title = "Calendar"
	}
	for n := 1; ; n++ {
		cand := title
		switch {
		case n == 2:
			cand = title + " (Mac)"
		case n > 2:
			cand = fmt.Sprintf("%s (Mac %d)", title, n-1)
		}
		cand = dav.DAVResourceName(cand, "")
		taken := used[cand]
		for i := range cols {
			c := &cols[i]
			if c.Kind == kindCal && c.Name == cand && c.DeletedAt == nil && c.ParentID == "" && ownCollection(c) {
				taken = taken || !dav.ParseCalendarColMeta(c.Metadata).Mirror
				break
			}
		}
		if !taken {
			return cand
		}
	}
}
