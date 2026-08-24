package agent

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
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
	kindContact = "contact"
	kindCal     = "calendar"
	kindBook    = "addressbook"
	kindPhoto   = "photo"
	kindPhotos  = "photos"
)

type Agent struct {
	Client   *protocol.Client
	Map      *Map
	Sel      Selection
	Cal      CalendarSource
	Contacts ContactSource
	Photos   PhotoSource
	Log      *slog.Logger

	routes map[string]route
}

type route struct {
	localID string
	kind    string
}

func (a *Agent) SyncOnce(ctx context.Context) error {
	if a.Log == nil {
		a.Log = slog.Default()
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
		for i := range res.Changes {
			if err := a.applyChange(ctx, res.Changes[i]); err != nil {
				a.Log.Warn("apply_change", "err", err.Error(), "object", res.Changes[i].ObjectID)
			}
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
			if rt.kind == kindContact && a.Contacts != nil {
				_ = a.Contacts.DeleteContact(existing.LocalID)
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
	switch rt.kind {
	case kindEvent:
		if a.Cal == nil {
			return nil
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
	case kindContact:
		if a.Contacts == nil {
			return nil
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
	default:
		return nil
	}
	return a.Map.Put(Mapping{
		LocalID: localID, Kind: rt.kind, ObjectID: ch.ObjectID, CollectionID: ch.CollectionID,
		ContentHash: ch.ContentHash, Revision: ch.Revision, StartMS: startMS,
	})
}

func (a *Agent) pushLocal(ctx context.Context) error {
	if err := a.pushCalendars(ctx); err != nil {
		return err
	}
	if err := a.pushContacts(ctx); err != nil {
		return err
	}
	return a.pushPhotos(ctx)
}

func (a *Agent) pushCalendars(ctx context.Context) error {
	if a.Cal == nil {
		return nil
	}
	now := time.Now()
	start := now.AddDate(0, 0, -a.Sel.WindowDays)
	end := now.AddDate(0, 0, a.Sel.WindowDays)
	for _, sel := range a.Sel.Calendars {
		if err := ctx.Err(); err != nil {
			return err
		}
		col, err := a.ensureCollection(kindCal, sel.Title)
		if err != nil {
			return err
		}
		events, err := a.Cal.ListEvents(sel.LocalID, start, end)
		if err != nil {
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
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.ID] = true
		if err := a.pushContact(col.ID, c); err != nil {
			a.Log.Warn("push_contact", "err", err.Error(), "local", c.ID)
		}
	}
	mapped, err := a.Map.ForCollection(col.ID)
	if err != nil {
		return err
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

func (a *Agent) pushEvent(collectionID string, ev LocalEvent) error {
	hash := ncrypto.SHA256Hex(ev.ICS)
	row, err := a.Map.ByLocal(kindEvent, ev.ID)
	if err == nil && row.ContentHash == hash {
		return nil
	}
	if err := a.Client.PutBlob(hash, ev.ICS); err != nil {
		return err
	}
	uid := ev.UID
	if uid == "" {
		uid = UIDFromICS(ev.ICS)
	}
	if uid == "" {
		uid = ev.ID
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindEvent,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         int64(len(ev.ICS)),
		Metadata:     dav.EncodeEventMeta(dav.EventMeta{Name: uid + ".ics", UID: uid, Comp: "VEVENT"}),
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
		return errors.New("push event rejected")
	}
	start := ev.StartMS
	if start == 0 {
		start = EventStartMS(ev.ICS)
	}
	return a.Map.Put(Mapping{
		LocalID: ev.ID, Kind: kindEvent, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision, StartMS: start,
	})
}

func (a *Agent) pushContact(collectionID string, c LocalContact) error {
	hash := ncrypto.SHA256Hex(c.VCard)
	row, err := a.Map.ByLocal(kindContact, c.ID)
	if err == nil && row.ContentHash == hash {
		return nil
	}
	if err := a.Client.PutBlob(hash, c.VCard); err != nil {
		return err
	}
	uid := c.UID
	if uid == "" {
		uid = UIDFromVCard(c.VCard)
	}
	if uid == "" {
		uid = c.ID
	}
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindContact,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         int64(len(c.VCard)),
		Metadata:     dav.EncodeContactMeta(dav.ContactMeta{Name: uid + ".vcf", UID: uid}),
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
		return errors.New("push contact rejected")
	}
	return a.Map.Put(Mapping{
		LocalID: c.ID, Kind: kindContact, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision,
	})
}

func (a *Agent) pushPhotos(ctx context.Context) error {
	if a.Photos == nil || !a.Sel.Photos.Enabled {
		return nil
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
	seen := map[string]bool{}
	for _, p := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[p.ID] = true
		if err := a.pushPhoto(col.ID, p); err != nil {
			a.Log.Warn("push_photo", "err", err.Error(), "local", p.ID)
		}
	}
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

func (a *Agent) pushPhoto(collectionID string, p LocalPhoto) error {
	row, mapErr := a.Map.ByLocal(kindPhoto, p.ID)
	orig := p.Original
	if len(orig) == 0 {
		if mapErr == nil {
			return nil
		}
		var err error
		orig, err = a.Photos.ReadOriginal(p.ID)
		if err != nil {
			return err
		}
	} else if mapErr == nil && row.ContentHash == ncrypto.SHA256Hex(orig) {
		return nil
	}
	prep, err := photos.Prepare(orig, p.Filename, photos.DefaultOptions())
	if err != nil {
		return err
	}
	prep.Meta.Albums = p.Albums
	if p.TakenAtMS != 0 && prep.Meta.TakenAtMS == 0 {
		prep.Meta.TakenAtMS = p.TakenAtMS
	}
	origHash := ncrypto.SHA256Hex(prep.Original)
	prep.Meta.Checksum = origHash
	if err := a.Client.PutBlob(origHash, prep.Original); err != nil {
		return err
	}
	if len(prep.Preview) > 0 {
		ph := ncrypto.SHA256Hex(prep.Preview)
		if err := a.Client.PutBlob(ph, prep.Preview); err != nil {
			return err
		}
		prep.Meta.PreviewHash = ph
	}
	if len(prep.Thumb) > 0 {
		th := ncrypto.SHA256Hex(prep.Thumb)
		if err := a.Client.PutBlob(th, prep.Thumb); err != nil {
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
		ContentHash: origHash, Revision: res[0].Revision, StartMS: prep.Meta.TakenAtMS,
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
	if a.Sel.SyncContacts {
		col, err := a.ensureCollection(kindBook, "Contacts")
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{kind: kindContact}
	}
	return nil
}

func (a *Agent) ensureCollection(kind, name string) (*syncengine.Collection, error) {
	if name == "" {
		name = "Untitled"
	}
	cols, err := a.Client.Collections()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		if cols[i].Kind == kind && cols[i].Name == name && cols[i].DeletedAt == nil {
			return &cols[i], nil
		}
	}
	return a.Client.EnsureCollection(kind, name)
}

func (a *Agent) Run(ctx context.Context) error {
	if err := a.SyncOnce(ctx); err != nil {
		a.Log.Error("sync", "err", err.Error())
	}
	d := time.Duration(a.Sel.IntervalSeconds) * time.Second
	if d < 15*time.Second {
		d = 15 * time.Second
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := a.SyncOnce(ctx); err != nil {
				a.Log.Error("sync", "err", err.Error())
			}
		}
	}
}
