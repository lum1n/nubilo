package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/ids"
	"nubilo/internal/photos"
	"nubilo/internal/protocol"
	"nubilo/internal/syncengine"
)

const (
	kindEvent   = "event"
	kindTodo    = "todo"
	kindContact = "contact"
	kindCal     = "calendar"
	kindBook    = "addressbook"
	kindPhoto   = "photo"
	kindPhotos  = "photos"
	kindFile    = "file"
	kindFiles   = "files"
)

type Agent struct {
	Client    *protocol.Client
	Map       *Map
	Sel       Selection
	SelPath   string // agent.json; reloaded at the start of each SyncOnce
	Cal       CalendarSource
	Reminders ReminderSource
	Contacts  ContactSource
	Photos    PhotoSource
	Files     FileSource
	Log       *slog.Logger

	routes map[string]route
}

type route struct {
	localID  string
	kind     string
	rootPath string // selected folder absolute path (files)
	relDir   string // relative dir of this collection within root (files)
}

func (a *Agent) reloadSelection() error {
	if a.SelPath == "" {
		return nil
	}
	sel, err := LoadSelection(a.SelPath)
	if err != nil {
		return err
	}
	a.Sel = sel
	return nil
}

func (a *Agent) SyncOnce(ctx context.Context) error {
	if a.Log == nil {
		a.Log = slog.Default()
	}
	if err := a.reloadSelection(); err != nil {
		return err
	}
	if err := a.bindRoutes(); err != nil {
		return err
	}
	cursor := a.Map.Cursor()
	hello, err := a.Client.Hello(cursor, false)
	if err != nil {
		return err
	}
	if hello.NeedReconcile {
		if err := a.reconcile(ctx); err != nil {
			return err
		}
	}
	if err := a.pull(ctx, cursor); err != nil {
		return err
	}
	return a.pushLocal(ctx)
}

func (a *Agent) pull(ctx context.Context, since int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := a.Client.Changes(since, 200, "")
		if err != nil {
			return err
		}
		if res.NeedReconcile {
			if err := a.reconcile(ctx); err != nil {
				return err
			}
			return a.pull(ctx, a.Map.Cursor())
		}
		failed := false
		for i := range res.Changes {
			if err := a.applyChange(ctx, res.Changes[i]); err != nil {
				a.Log.Warn("apply_change", "err", err.Error(), "object", res.Changes[i].ObjectID)
				failed = true
			}
		}
		if failed {
			return errors.New("apply_change failed")
		}
		if res.NextSeq > since {
			if err := a.Client.Ack(res.NextSeq); err != nil {
				return err
			}
			if err := a.Map.SetCursor(res.NextSeq); err != nil {
				return err
			}
			since = res.NextSeq
		}
		if !res.HasMore {
			return nil
		}
	}
}

func (a *Agent) applyChange(ctx context.Context, ch syncengine.Change) error {
	rt, ok := a.routes[ch.CollectionID]
	if !ok {
		return nil
	}
	existing, mapErr := a.Map.ByObject(ch.ObjectID)
	if ch.Op == syncengine.OpDelete || ch.Deleted {
		if mapErr == nil {
			if rt.kind == kindEvent && a.Cal != nil {
				_ = a.Cal.DeleteEvent(existing.LocalID)
			}
			if rt.kind == kindTodo && a.Reminders != nil {
				_ = a.Reminders.DeleteReminder(existing.LocalID)
			}
			if rt.kind == kindContact && a.Contacts != nil {
				_ = a.Contacts.DeleteContact(existing.LocalID)
				a.Map.DeleteContactCache(existing.LocalID)
			}
			if rt.kind == kindFile && a.Files != nil {
				_ = a.Files.DeleteFile(existing.LocalID)
			}
			if rt.kind == kindPhoto {
				// Do not delete from Photos.app on remote tombstone (safe default).
			}
			return a.Map.DeleteObject(ch.ObjectID)
		}
		return nil
	}
	if mapErr == nil && existing.Revision >= ch.Revision && existing.ContentHash == ch.ContentHash {
		return nil
	}
	var payload []byte
	if ch.BlobID != "" {
		var err error
		payload, err = a.Client.GetBlob(ch.BlobID)
		if err != nil {
			return err
		}
	}
	var localID string
	var startMS int64
	var modMS int64
	uid := ""
	name := ""
	switch rt.kind {
	case kindEvent:
		if a.Cal == nil {
			return nil
		}
		meta := dav.ParseEventMeta(ch.Metadata)
		uid, name = meta.UID, meta.Name
		if uid == "" {
			uid = UIDFromICS(payload)
		}
		prev := ""
		if mapErr == nil {
			prev = existing.LocalID
		}
		id, err := a.Cal.UpsertEvent(rt.localID, prev, payload)
		if err != nil {
			return err
		}
		localID = id
		startMS = EventStartMS(payload)
	case kindTodo:
		if a.Reminders == nil {
			return nil
		}
		meta := dav.ParseEventMeta(ch.Metadata)
		uid, name = meta.UID, meta.Name
		if uid == "" {
			uid = UIDFromICS(payload)
		}
		prev := ""
		if mapErr == nil {
			prev = existing.LocalID
		}
		id, err := a.Reminders.UpsertReminder(rt.localID, prev, payload)
		if err != nil {
			return err
		}
		localID = id
		startMS = TodoDueMS(payload)
	case kindContact:
		if a.Contacts == nil {
			return nil
		}
		meta := dav.ParseContactMeta(ch.Metadata)
		uid, name = meta.UID, meta.Name
		if uid == "" {
			uid = UIDFromVCard(payload)
		}
		prev := ""
		if mapErr == nil {
			prev = existing.LocalID
		}
		id, err := a.Contacts.UpsertContact(prev, payload)
		if err != nil {
			return err
		}
		localID = id
		_ = a.Map.SaveContactCache(localID, payload)
	case kindFile:
		if a.Files == nil || rt.rootPath == "" {
			return nil
		}
		meta := dav.ParseFileMeta(ch.Metadata)
		fname := meta.Name
		if skipFileName(fname) {
			return nil
		}
		abs := filepath.Join(rt.rootPath, filepath.FromSlash(rt.relDir), fname)
		if err := a.Files.WriteFile(abs, payload); err != nil {
			return err
		}
		localID = abs
	case kindPhoto:
		if a.Photos == nil {
			return nil
		}
		if mapErr == nil {
			// Already mapped locally; last-writer-wins only if content changed.
			if existing.ContentHash == ch.ContentHash {
				return a.Map.Put(Mapping{
					LocalID: existing.LocalID, Kind: kindPhoto, ObjectID: ch.ObjectID, CollectionID: ch.CollectionID,
					ContentHash: ch.ContentHash, Revision: ch.Revision, StartMS: existing.StartMS, ModMS: existing.ModMS,
					UID: existing.UID, Name: existing.Name, LocalHash: existing.LocalHash,
				})
			}
			// Content changed remotely: import a new asset (do not mutate existing Photos).
		}
		pm := photos.ParseMeta(ch.Metadata)
		pname := pm.Name
		if pname == "" {
			pname = "photo.bin"
		}
		albumID := ""
		if len(a.Sel.Photos.Albums) > 0 {
			albumID = a.Sel.Photos.Albums[0]
		}
		id, err := a.Photos.ImportOriginal(payload, pname, albumID)
		if err != nil {
			return err
		}
		localID = id
		startMS = pm.TakenAtMS
		modMS = time.Now().UnixMilli()
	default:
		return nil
	}
	return a.Map.Put(Mapping{
		LocalID: localID, Kind: rt.kind, ObjectID: ch.ObjectID, CollectionID: ch.CollectionID,
		ContentHash: ch.ContentHash, Revision: ch.Revision, StartMS: startMS, ModMS: modMS,
		UID: uid, Name: name, LocalHash: "",
	})
}

func (a *Agent) pushLocal(ctx context.Context) error {
	if err := a.pushCalendars(ctx); err != nil {
		return err
	}
	if err := a.pushReminders(ctx); err != nil {
		return err
	}
	if err := a.pushContacts(ctx); err != nil {
		return err
	}
	if err := a.pushPhotos(ctx); err != nil {
		return err
	}
	return a.pushFiles(ctx)
}

func (a *Agent) pushCalendars(ctx context.Context) error {
	if a.Cal == nil {
		return nil
	}
	now := time.Now()
	start := now.AddDate(0, 0, -a.Sel.WindowDays)
	end := now.AddDate(0, 0, a.Sel.WindowDays)
	colorByID := map[string]string{}
	if cals, err := a.Cal.ListCalendars(); err == nil {
		for _, c := range cals {
			if c.Color != "" {
				colorByID[c.ID] = c.Color
			}
		}
	}
	for _, sel := range a.Sel.Calendars {
		if err := ctx.Err(); err != nil {
			return err
		}
		col, err := a.ensureCollection(kindCal, sel.Title)
		if err != nil {
			return err
		}
		if hex := colorByID[sel.LocalID]; hex != "" {
			if err := a.syncCollectionColor(col, hex); err != nil {
				a.Log.Warn("calendar_color", "name", sel.Title, "err", err.Error())
			}
		}
		events, err := a.Cal.ListEvents(sel.LocalID, start, end)
		if err != nil {
			a.Log.Warn("list_events_failed", "calendar", sel.Title, "err", err.Error())
			continue
		}
		a.Log.Info("push_calendar", "name", sel.Title, "events", len(events))
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
		winStart, winEnd := start.UnixMilli(), end.UnixMilli()
		for _, row := range mapped {
			if seen[row.LocalID] {
				continue
			}
			if row.StartMS == 0 || row.StartMS < winStart || row.StartMS > winEnd {
				continue
			}
			if err := a.pushDelete(row); err != nil {
				a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
			}
		}
	}
	return nil
}

func (a *Agent) pushReminders(ctx context.Context) error {
	if a.Reminders == nil || len(a.Sel.Reminders) == 0 {
		return nil
	}
	now := time.Now()
	start := now.AddDate(0, 0, -a.Sel.WindowDays)
	end := now.AddDate(0, 0, a.Sel.WindowDays)
	colorByID := map[string]string{}
	if lists, err := a.Reminders.ListReminderLists(); err == nil {
		for _, c := range lists {
			if c.Color != "" {
				colorByID[c.ID] = c.Color
			}
		}
	}
	for _, sel := range a.Sel.Reminders {
		if err := ctx.Err(); err != nil {
			return err
		}
		col, err := a.ensureReminderCollection(sel.Title, colorByID[sel.LocalID])
		if err != nil {
			return err
		}
		todos, err := a.Reminders.ListReminders(sel.LocalID, start, end)
		if err != nil {
			a.Log.Warn("list_reminders_failed", "list", sel.Title, "err", err.Error())
			continue
		}
		a.Log.Info("push_reminders", "name", sel.Title, "todos", len(todos))
		seen := map[string]bool{}
		for _, td := range todos {
			seen[td.ID] = true
			if err := a.pushTodo(col.ID, td); err != nil {
				a.Log.Warn("push_todo", "err", err.Error(), "local", td.ID)
			}
		}
		mapped, err := a.Map.ForCollection(col.ID)
		if err != nil {
			return err
		}
		winStart, winEnd := start.UnixMilli(), end.UnixMilli()
		for _, row := range mapped {
			if seen[row.LocalID] {
				continue
			}
			// Incomplete undated todos use StartMS=0; always delete if missing from list.
			if row.StartMS != 0 && (row.StartMS < winStart || row.StartMS > winEnd) {
				continue
			}
			if err := a.pushDelete(row); err != nil {
				a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
			}
		}
	}
	return nil
}

func (a *Agent) pushContacts(ctx context.Context) error {
	if a.Contacts == nil || !a.Sel.SyncContacts {
		return nil
	}
	col, err := a.ensureCollection(kindBook, "Contacts")
	if err != nil {
		return err
	}
	list, err := a.Contacts.ListContacts()
	if err != nil {
		a.Log.Warn("list_contacts_failed", "err", err.Error())
		return nil
	}
	mapped, err := a.Map.ForCollection(col.ID)
	if err != nil {
		return err
	}
	skipDeletes := false
	if a.Map.MetaInt("contacts_id_mode") != 1 {
		list, mapped, skipDeletes = a.remapContacts(list, mapped)
		if err := a.Map.SetMetaInt("contacts_id_mode", 1); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.ID] = true
		if err := a.pushContact(col.ID, c); err != nil {
			a.Log.Warn("push_contact", "err", err.Error(), "local", c.ID)
		}
	}
	if len(list) == 0 && len(mapped) > 0 {
		a.Log.Warn("contacts_empty_listing", "mapped", len(mapped))
		return nil
	}
	if skipDeletes {
		return nil
	}
	for _, row := range mapped {
		if seen[row.LocalID] {
			continue
		}
		if err := a.pushDelete(row); err != nil {
			a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
		}
	}
	return nil
}

// remapContacts rebinds idmap rows whose unified identifiers no longer match
// the non-unified container listing. Returns updated list/mapped and whether
// deletes should be skipped for this run.
func (a *Agent) remapContacts(list []LocalContact, mapped []Mapping) ([]LocalContact, []Mapping, bool) {
	byID := map[string]bool{}
	for _, c := range list {
		byID[c.ID] = true
	}
	used := map[string]bool{}
	fpList := map[string]string{}
	for _, c := range list {
		fpList[c.ID] = ContactFingerprint(ParseContactVCard(c.VCard))
	}
	outMapped := make([]Mapping, 0, len(mapped))
	for _, row := range mapped {
		if byID[row.LocalID] {
			outMapped = append(outMapped, row)
			used[row.LocalID] = true
			continue
		}
		want := ""
		if cached := a.Map.LoadContactCache(row.LocalID); len(cached) > 0 {
			want = ContactFingerprint(ParseContactVCard(cached))
		}
		if want == "" {
			outMapped = append(outMapped, row)
			continue
		}
		matched := ""
		for _, c := range list {
			if used[c.ID] {
				continue
			}
			if fpList[c.ID] == want {
				matched = c.ID
				break
			}
		}
		if matched == "" {
			outMapped = append(outMapped, row)
			continue
		}
		oldID := row.LocalID
		row.LocalID = matched
		row.LocalHash = ""
		if err := a.Map.Put(row); err != nil {
			a.Log.Warn("contacts_remap", "err", err.Error(), "from", oldID, "to", matched)
			row.LocalID = oldID
			outMapped = append(outMapped, row)
			continue
		}
		if cached := a.Map.LoadContactCache(oldID); len(cached) > 0 {
			_ = a.Map.SaveContactCache(matched, cached)
			a.Map.DeleteContactCache(oldID)
		}
		used[matched] = true
		outMapped = append(outMapped, row)
	}
	return list, outMapped, true // skip deletes on first container-mode run
}

func (a *Agent) pushEvent(collectionID string, ev LocalEvent) error {
	rawHash := ncrypto.SHA256Hex(ev.ICS)
	row, mapErr := a.Map.ByLocal(kindEvent, ev.ID)
	if mapErr == nil {
		if row.LocalHash == "" {
			row.LocalHash = rawHash
			return a.Map.Put(row)
		}
		if row.LocalHash == rawHash {
			return nil
		}
	} else if !errors.Is(mapErr, sql.ErrNoRows) {
		return mapErr
	}

	ics := ev.ICS
	uid := ev.UID
	name := ""
	if mapErr == nil && row.UID != "" {
		uid = row.UID
		ics = SetICSUID(ev.ICS, row.UID)
		name = row.Name
	}
	if uid == "" {
		uid = UIDFromICS(ics)
	}
	if uid == "" {
		uid = ev.ID
	}
	if name == "" {
		name = dav.DAVResourceName(uid+".ics", ".ics")
	}
	hash := ncrypto.SHA256Hex(ics)
	if err := a.putBlob(hash, ics); err != nil {
		return err
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindEvent,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         int64(len(ics)),
		Metadata:     dav.EncodeEventMeta(dav.EventMeta{Name: name, UID: uid, Comp: "VEVENT"}),
	}
	if errors.Is(mapErr, sql.ErrNoRows) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else {
		in.ObjectID = row.ObjectID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = row.Revision
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return errors.New("push event rejected")
	}
	if res[0].Status == "conflict" {
		a.Log.Warn("push_conflict", "object", in.ObjectID, "server_revision", res[0].ServerRevision, "server_hash", res[0].ServerContentHash)
		return nil
	}
	if res[0].Status != "ok" {
		return errors.New("push event rejected")
	}
	start := ev.StartMS
	if start == 0 {
		start = EventStartMS(ics)
	}
	return a.Map.Put(Mapping{
		LocalID: ev.ID, Kind: kindEvent, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision, StartMS: start,
		UID: uid, Name: name, LocalHash: rawHash,
	})
}

func (a *Agent) pushTodo(collectionID string, td LocalTodo) error {
	rawHash := ncrypto.SHA256Hex(td.ICS)
	row, mapErr := a.Map.ByLocal(kindTodo, td.ID)
	if mapErr == nil {
		if row.LocalHash == "" {
			row.LocalHash = rawHash
			return a.Map.Put(row)
		}
		if row.LocalHash == rawHash {
			return nil
		}
	} else if !errors.Is(mapErr, sql.ErrNoRows) {
		return mapErr
	}

	ics := td.ICS
	uid := td.UID
	name := ""
	if mapErr == nil && row.UID != "" {
		uid = row.UID
		ics = SetICSUID(td.ICS, row.UID)
		name = row.Name
	}
	if uid == "" {
		uid = UIDFromICS(ics)
	}
	if uid == "" {
		uid = td.ID
	}
	if name == "" {
		name = dav.DAVResourceName(uid+".ics", ".ics")
	}
	hash := ncrypto.SHA256Hex(ics)
	if err := a.putBlob(hash, ics); err != nil {
		return err
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindTodo,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         int64(len(ics)),
		Metadata:     dav.EncodeEventMeta(dav.EventMeta{Name: name, UID: uid, Comp: "VTODO"}),
	}
	if errors.Is(mapErr, sql.ErrNoRows) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else {
		in.ObjectID = row.ObjectID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = row.Revision
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return errors.New("push todo rejected")
	}
	if res[0].Status == "conflict" {
		a.Log.Warn("push_conflict", "object", in.ObjectID, "server_revision", res[0].ServerRevision, "server_hash", res[0].ServerContentHash)
		return nil
	}
	if res[0].Status != "ok" {
		return errors.New("push todo rejected")
	}
	due := td.DueMS
	if due == 0 {
		due = TodoDueMS(ics)
	}
	return a.Map.Put(Mapping{
		LocalID: td.ID, Kind: kindTodo, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision, StartMS: due,
		UID: uid, Name: name, LocalHash: rawHash,
	})
}

func (a *Agent) pushContact(collectionID string, c LocalContact) error {
	rawHash := ncrypto.SHA256Hex(c.VCard)
	row, mapErr := a.Map.ByLocal(kindContact, c.ID)
	if mapErr == nil {
		if row.LocalHash == "" {
			row.LocalHash = rawHash
			return a.Map.Put(row)
		}
		if row.LocalHash == rawHash {
			return nil
		}
	} else if !errors.Is(mapErr, sql.ErrNoRows) {
		return mapErr
	}

	spec := ParseContactVCard(c.VCard)
	if mapErr == nil && row.UID != "" && spec.UID == "" {
		spec.UID = row.UID
	}
	vcf := c.VCard
	if cached := a.Map.LoadContactCache(c.ID); len(cached) > 0 {
		vcf = MergeContactVCard(cached, ParseContactVCard(c.VCard))
	} else if mapErr == nil && row.UID != "" {
		spec.UID = row.UID
		vcf = EncodeContactVCard(spec)
	}
	uid := UIDFromVCard(vcf)
	if uid == "" {
		uid = c.UID
	}
	if uid == "" {
		uid = c.ID
	}
	name := ""
	if mapErr == nil && row.Name != "" {
		name = row.Name
	}
	if name == "" {
		name = dav.DAVResourceName(uid+".vcf", ".vcf")
	}
	hash := ncrypto.SHA256Hex(vcf)
	if err := a.putBlob(hash, vcf); err != nil {
		return err
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindContact,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         int64(len(vcf)),
		Metadata:     dav.EncodeContactMeta(dav.ContactMetaFromVCard(name, uid, vcf)),
	}
	if errors.Is(mapErr, sql.ErrNoRows) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else {
		in.ObjectID = row.ObjectID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = row.Revision
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return errors.New("push contact rejected")
	}
	if res[0].Status == "conflict" {
		a.Log.Warn("push_conflict", "object", in.ObjectID, "server_revision", res[0].ServerRevision, "server_hash", res[0].ServerContentHash)
		return nil
	}
	if res[0].Status != "ok" {
		return errors.New("push contact rejected")
	}
	_ = a.Map.SaveContactCache(c.ID, vcf)
	return a.Map.Put(Mapping{
		LocalID: c.ID, Kind: kindContact, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision,
		UID: uid, Name: name, LocalHash: rawHash,
	})
}

func (a *Agent) pushPhotos(ctx context.Context) error {
	if a.Photos == nil || !a.Sel.Photos.Enabled {
		return nil
	}
	if st := PhotosAuthStatus(); st == "limited" {
		a.Log.Warn("photos_limited_access",
			"hint", "only TCC-selected photos sync; run: nubilo agent authorize → Allow Full Access")
	}
	col, err := a.ensureCollection(kindPhotos, photos.DefaultName)
	if err != nil {
		return err
	}
	list, err := a.Photos.ListPhotos(a.Sel.PhotoFilter())
	if err != nil {
		a.Log.Warn("list_photos_failed", "err", err.Error())
		return nil
	}
	a.Log.Info("photos_list", "n", len(list), "source", a.Sel.Photos.Source)
	seen := map[string]bool{}
	ok, fail := 0, 0
	for _, p := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[p.ID] = true
		if err := a.pushPhoto(col.ID, p); err != nil {
			fail++
			a.Log.Warn("push_photo", "err", err.Error(), "local", p.ID, "kind", p.Kind, "filename", p.Filename)
		} else {
			ok++
		}
	}
	a.Log.Info("photos_push", "ok", ok, "fail", fail)
	mapped, err := a.Map.ForCollection(col.ID)
	if err != nil {
		return err
	}
	flt := a.Sel.PhotoFilter()
	for _, row := range mapped {
		if seen[row.LocalID] {
			continue
		}
		if flt.Source == "dates" {
			if row.StartMS == 0 {
				continue
			}
			if flt.AfterMS > 0 && row.StartMS < flt.AfterMS {
				continue
			}
			if flt.BeforeMS > 0 && row.StartMS > flt.BeforeMS {
				continue
			}
		}
		if err := a.pushDelete(row); err != nil {
			a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
		}
	}
	return nil
}

func (a *Agent) pushFiles(ctx context.Context) error {
	if a.Files == nil || !a.Sel.Files.Enabled {
		return nil
	}
	for _, folder := range a.Sel.Files.Folders {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.pushFileFolder(ctx, folder); err != nil {
			a.Log.Warn("push_files", "path", folder.Path, "err", err.Error())
		}
	}
	return nil
}

func (a *Agent) pushFileFolder(ctx context.Context, folder FileFolderSel) error {
	root, err := filepath.Abs(folder.Path)
	if err != nil {
		return err
	}
	name := folder.Name
	if name == "" {
		name = filepath.Base(root)
	}
	rootCol, err := a.ensureCollection(kindFiles, name)
	if err != nil {
		return err
	}
	a.routes[rootCol.ID] = route{kind: kindFile, rootPath: root, relDir: ""}
	list, err := a.Files.ListFiles(root)
	if err != nil {
		a.Log.Warn("list_files_failed", "path", root, "err", err.Error())
		return nil
	}
	a.Log.Info("files_list", "path", root, "n", len(list))
	seen := map[string]bool{}
	colCache := map[string]string{"": rootCol.ID}
	ok, fail := 0, 0
	for _, f := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := slashRelDir(f.RelPath)
		colID, err := a.ensureFileDirCollection(root, rootCol.ID, dir, colCache)
		if err != nil {
			fail++
			a.Log.Warn("ensure_file_dir", "dir", dir, "err", err.Error())
			continue
		}
		seen[f.AbsPath] = true
		if err := a.pushFile(colID, f); err != nil {
			fail++
			a.Log.Warn("push_file", "err", err.Error(), "local", f.AbsPath)
		} else {
			ok++
		}
	}
	a.Log.Info("files_push", "path", root, "ok", ok, "fail", fail)
	for colID, rt := range a.routes {
		if rt.kind != kindFile || rt.rootPath != root {
			continue
		}
		mapped, err := a.Map.ForCollection(colID)
		if err != nil {
			return err
		}
		for _, row := range mapped {
			if seen[row.LocalID] {
				continue
			}
			if !strings.HasPrefix(row.LocalID, root+string(os.PathSeparator)) && row.LocalID != root {
				continue
			}
			if err := a.pushDelete(row); err != nil {
				a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
			}
		}
	}
	return nil
}

func (a *Agent) ensureFileDirCollection(root, rootColID, relDir string, cache map[string]string) (string, error) {
	if id, ok := cache[relDir]; ok {
		return id, nil
	}
	parts := strings.Split(relDir, "/")
	parentID := rootColID
	cur := ""
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		if id, ok := cache[cur]; ok {
			parentID = id
			continue
		}
		col, err := a.ensureChildFilesCollection(parentID, p)
		if err != nil {
			return "", err
		}
		a.routes[col.ID] = route{kind: kindFile, rootPath: root, relDir: cur}
		cache[cur] = col.ID
		parentID = col.ID
	}
	cache[relDir] = parentID
	return parentID, nil
}

// slashRelDir returns the parent directory of a slash-separated relative path.
func slashRelDir(rel string) string {
	rel = filepath.ToSlash(rel)
	i := strings.LastIndex(rel, "/")
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

func (a *Agent) pushFile(collectionID string, f LocalFile) error {
	if skipFileName(f.Name) {
		return nil
	}
	hash := ncrypto.SHA256Hex(f.Data)
	row, err := a.Map.ByLocal(kindFile, f.AbsPath)
	if err == nil && row.ContentHash == hash && row.CollectionID == collectionID {
		return nil
	}
	if err := a.putBlob(hash, f.Data); err != nil {
		return err
	}
	mime := http.DetectContentType(f.Data)
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindFile,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         f.Size,
		Metadata:     dav.EncodeFileMeta(dav.FileMeta{Name: f.Name, MIME: mime}),
		Force:        true,
	}
	if errors.Is(err, sql.ErrNoRows) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else if err != nil {
		return err
	} else {
		in.ObjectID = row.ObjectID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = row.Revision
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		return errors.New("push file rejected")
	}
	return a.Map.Put(Mapping{
		LocalID: f.AbsPath, Kind: kindFile, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision,
	})
}

func (a *Agent) putBlob(hash string, payload []byte) error {
	if err := a.Client.PutBlob(hash, payload); err != nil {
		return fmt.Errorf("%w (bytes=%d)", err, len(payload))
	}
	return nil
}

func (a *Agent) pushPhoto(collectionID string, p LocalPhoto) error {
	row, mapErr := a.Map.ByLocal(kindPhoto, p.ID)
	if mapErr == nil {
		// Skip re-export when mapped and PhotoKit modificationDate unchanged.
		if p.ModMS > 0 && row.ModMS == p.ModMS {
			return nil
		}
		if p.ModMS == 0 && len(p.Original) == 0 {
			// No mod signal and no in-memory bytes: keep prior skip-if-mapped behavior.
			return nil
		}
	}
	orig := p.Original
	if len(orig) == 0 {
		var err error
		orig, err = a.Photos.ReadOriginal(p.ID)
		if err != nil {
			return err
		}
	}
	if mapErr == nil && len(orig) > 0 && row.ContentHash == ncrypto.SHA256Hex(orig) {
		if p.ModMS > 0 && row.ModMS != p.ModMS {
			row.ModMS = p.ModMS
			return a.Map.Put(row)
		}
		return nil
	}
	kind := p.Kind
	if kind == "" {
		kind = "image"
	}
	prep, err := photos.PrepareKind(orig, p.Filename, kind, p.DurationMS, photos.DefaultOptions())
	if err != nil {
		return err
	}
	prep.Meta.Albums = p.Albums
	if p.TakenAtMS != 0 && prep.Meta.TakenAtMS == 0 {
		prep.Meta.TakenAtMS = p.TakenAtMS
	}
	if kind == "live" {
		live := p.LiveMovie
		if len(live) == 0 {
			live, _ = a.Photos.ReadLiveMovie(p.ID)
		}
		if len(live) > 0 {
			lh := ncrypto.SHA256Hex(live)
			if err := a.putBlob(lh, live); err != nil {
				return err
			}
			prep.Meta.LivePairHash = lh
			prep.Meta.Kind = "live"
		}
	}
	origHash := ncrypto.SHA256Hex(prep.Original)
	prep.Meta.Checksum = origHash
	if err := a.putBlob(origHash, prep.Original); err != nil {
		return err
	}
	if len(prep.Preview) > 0 {
		ph := ncrypto.SHA256Hex(prep.Preview)
		if err := a.putBlob(ph, prep.Preview); err != nil {
			return err
		}
		prep.Meta.PreviewHash = ph
	}
	if len(prep.Thumb) > 0 {
		th := ncrypto.SHA256Hex(prep.Thumb)
		if err := a.putBlob(th, prep.Thumb); err != nil {
			return err
		}
		prep.Meta.ThumbHash = th
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindPhoto,
		ContentHash:  origHash,
		BlobID:       origHash,
		Size:         int64(len(prep.Original)),
		Metadata:     photos.EncodeMeta(prep.Meta),
		Force:        true,
	}
	if errors.Is(mapErr, sql.ErrNoRows) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else if mapErr != nil {
		return mapErr
	} else {
		in.ObjectID = row.ObjectID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = row.Revision
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		return errors.New("push photo rejected")
	}
	return a.Map.Put(Mapping{
		LocalID: p.ID, Kind: kindPhoto, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: origHash, Revision: res[0].Revision, StartMS: prep.Meta.TakenAtMS, ModMS: p.ModMS,
	})
}

func (a *Agent) pushDelete(row Mapping) error {
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{{
		ObjectID: row.ObjectID, CollectionID: row.CollectionID, Op: syncengine.OpDelete,
		BaseRevision: row.Revision, Force: true,
	}})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		return errors.New("push delete rejected")
	}
	if row.Kind == kindContact {
		a.Map.DeleteContactCache(row.LocalID)
	}
	return a.Map.DeleteObject(row.ObjectID)
}

func (a *Agent) reconcile(ctx context.Context) error {
	for _, sel := range a.Sel.Calendars {
		col, err := a.ensureCollection(kindCal, sel.Title)
		if err != nil {
			return err
		}
		if err := a.reconcileCollection(ctx, col.ID); err != nil {
			return err
		}
	}
	for _, sel := range a.Sel.Reminders {
		col, err := a.ensureReminderCollection(sel.Title, "")
		if err != nil {
			return err
		}
		if err := a.reconcileCollection(ctx, col.ID); err != nil {
			return err
		}
	}
	if a.Sel.SyncContacts {
		col, err := a.ensureCollection(kindBook, "Contacts")
		if err != nil {
			return err
		}
		if err := a.reconcileCollection(ctx, col.ID); err != nil {
			return err
		}
	}
	if a.Sel.Photos.Enabled {
		col, err := a.ensureCollection(kindPhotos, photos.DefaultName)
		if err != nil {
			return err
		}
		return a.reconcileCollection(ctx, col.ID)
	}
	return nil
}

func (a *Agent) reconcileCollection(ctx context.Context, collectionID string) error {
	inv, err := a.Map.Inventory(collectionID)
	if err != nil {
		return err
	}
	res, err := a.Client.Reconcile(collectionID, inv)
	if err != nil {
		return err
	}
	for _, id := range res.MissingOnServer {
		row, err := a.Map.ByObject(id)
		if err != nil {
			continue
		}
		_ = row
		// Local object not on server: next pushLocal will recreate it if still present in the source.
	}
	for _, id := range res.MissingOnClient {
		_ = a.applyRemoteObject(ctx, collectionID, id)
	}
	for _, mm := range res.Mismatch {
		_ = a.applyRemoteObject(ctx, collectionID, mm.ID)
	}
	return nil
}

func (a *Agent) applyRemoteObject(ctx context.Context, collectionID, objectID string) error {
	cur := int64(0)
	for {
		batch, err := a.Client.Changes(cur, 200, collectionID)
		if err != nil {
			return err
		}
		for _, ch := range batch.Changes {
			if ch.ObjectID == objectID {
				return a.applyChange(ctx, ch)
			}
		}
		if !batch.HasMore || batch.NextSeq <= cur {
			return nil
		}
		cur = batch.NextSeq
	}
}

func (a *Agent) bindRoutes() error {
	a.routes = map[string]route{}
	for _, sel := range a.Sel.Calendars {
		col, err := a.ensureCollection(kindCal, sel.Title)
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{localID: sel.LocalID, kind: kindEvent}
	}
	for _, sel := range a.Sel.Reminders {
		col, err := a.ensureReminderCollection(sel.Title, "")
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{localID: sel.LocalID, kind: kindTodo}
	}
	if a.Sel.SyncContacts {
		col, err := a.ensureCollection(kindBook, "Contacts")
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{kind: kindContact}
	}
	if a.Sel.Photos.Enabled {
		col, err := a.ensureCollection(kindPhotos, photos.DefaultName)
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{kind: kindPhoto}
	}
	if a.Sel.Files.Enabled && a.Files != nil {
		for _, folder := range a.Sel.Files.Folders {
			if err := a.bindFileFolder(folder); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) bindFileFolder(folder FileFolderSel) error {
	root, err := filepath.Abs(folder.Path)
	if err != nil {
		return err
	}
	name := folder.Name
	if name == "" {
		name = filepath.Base(root)
	}
	col, err := a.ensureCollection(kindFiles, name)
	if err != nil {
		return err
	}
	a.routes[col.ID] = route{kind: kindFile, rootPath: root, relDir: ""}
	return a.bindFileSubdirs(root, col.ID, "")
}

func (a *Agent) bindFileSubdirs(root, parentColID, relDir string) error {
	dir := filepath.Join(root, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
			continue
		}
		if !dav.ValidDisplayName(name) {
			continue
		}
		childRel := name
		if relDir != "" {
			childRel = relDir + "/" + name
		}
		col, err := a.ensureChildFilesCollection(parentColID, name)
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{kind: kindFile, rootPath: root, relDir: childRel}
		if err := a.bindFileSubdirs(root, col.ID, childRel); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) ensureChildFilesCollection(parentID, name string) (*syncengine.Collection, error) {
	name = dav.DAVResourceName(name, "")
	cols, err := a.Client.Collections()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		c := &cols[i]
		if c.Kind == kindFiles && c.Name == name && c.ParentID == parentID && c.DeletedAt == nil {
			return c, nil
		}
	}
	return a.Client.EnsureChildCollection(kindFiles, parentID, name, nil)
}

func (a *Agent) ensureCollection(kind, name string) (*syncengine.Collection, error) {
	if name == "" {
		name = "Untitled"
	}
	name = dav.DAVResourceName(name, "")
	cols, err := a.Client.Collections()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		if cols[i].Kind == kind && cols[i].Name == name && cols[i].DeletedAt == nil {
			return &cols[i], nil
		}
	}
	return a.Client.EnsureCollection(kind, name, nil)
}

func reminderDAVName(title string, cols []syncengine.Collection) string {
	base := dav.DAVResourceName(title, "")
	for i := range cols {
		c := &cols[i]
		if c.Kind != kindCal || c.DeletedAt != nil || c.Name != base {
			continue
		}
		if dav.ParseCalendarColMeta(c.Metadata).Comp == "VTODO" {
			return base
		}
		return dav.DAVResourceName(title+" Reminders", "")
	}
	return base
}

func (a *Agent) ensureReminderCollection(title, color string) (*syncengine.Collection, error) {
	if title == "" {
		title = "Reminders"
	}
	cols, err := a.Client.Collections()
	if err != nil {
		return nil, err
	}
	name := reminderDAVName(title, cols)
	color = dav.NormalizeCalendarColor(color)
	patch := dav.EncodeCalendarColMeta(dav.CalendarColMeta{Comp: "VTODO", Color: color})
	for i := range cols {
		c := &cols[i]
		if c.Kind != kindCal || c.Name != name || c.DeletedAt != nil {
			continue
		}
		meta := dav.ParseCalendarColMeta(c.Metadata)
		need := meta.Comp != "VTODO" || (color != "" && meta.Color != color)
		if !need {
			return c, nil
		}
		return a.Client.EnsureCollection(kindCal, name, patch)
	}
	return a.Client.EnsureCollection(kindCal, name, patch)
}

func (a *Agent) syncCollectionColor(col *syncengine.Collection, color string) error {
	color = dav.NormalizeCalendarColor(color)
	if color == "" {
		return nil
	}
	if dav.ParseCalendarColMeta(col.Metadata).Color == color {
		return nil
	}
	updated, err := a.Client.EnsureCollection(kindCal, col.Name, dav.EncodeCalendarColMeta(dav.CalendarColMeta{Color: color}))
	if err != nil {
		return err
	}
	*col = *updated
	return nil
}

func (a *Agent) runSync(ctx context.Context) {
	if err := a.SyncOnce(ctx); err != nil {
		a.Log.Error("sync", "err", err.Error())
		return
	}
	a.Log.Info("sync_ok", "calendars", len(a.Sel.Calendars), "reminders", len(a.Sel.Reminders), "contacts", a.Sel.SyncContacts, "photos", a.Sel.Photos.Enabled, "files", a.Sel.Files.Enabled)
}

func (a *Agent) Run(ctx context.Context) error {
	a.runSync(ctx)
	d := time.Duration(a.Sel.IntervalSeconds) * time.Second
	if d < 15*time.Second {
		d = 15 * time.Second
	}
	t := time.NewTicker(d)
	defer t.Stop()
	local := watchLocalChanges()
	var debounce *time.Timer
	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return ctx.Err()
		case <-t.C:
			a.runSync(ctx)
		case <-local:
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(2*time.Second, func() {
				a.runSync(ctx)
			})
		}
	}
}
