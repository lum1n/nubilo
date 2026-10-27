package dav

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"nubilo/internal/authz"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

const CalDAVPrefix = "/caldav"

const (
	calKind    = "calendar"
	eventKind  = "event"
	calHomeSeg = "calendars"
)

type CalDAV struct {
	Engine *syncengine.Engine
	Store  *store.Store
	Prefix string
}

func NewCalDAV(eng *syncengine.Engine, st *store.Store) *CalDAV {
	return &CalDAV{Engine: eng, Store: st, Prefix: CalDAVPrefix}
}

func (b *CalDAV) user(ctx context.Context) (*davUser, error) {
	if err := b.allow(ctx, false); err != nil {
		return nil, err
	}
	return currentUser(ctx, b.Engine)
}

func (b *CalDAV) CurrentUserPrincipal(ctx context.Context) (string, error) {
	u, err := b.user(ctx)
	if err != nil {
		return "", err
	}
	return homeBase(ctx, b.Prefix, u) + "/", nil
}

func (b *CalDAV) CalendarHomeSetPath(ctx context.Context) (string, error) {
	u, err := b.user(ctx)
	if err != nil {
		return "", err
	}
	return homeBase(ctx, b.Prefix, u) + "/" + calHomeSeg + "/", nil
}

// CreateCalendar creates a calendar owned by the caller. When the path
// segment is not a collection ID (Apple picks a UUID), it is kept as the
// calendar's slug so the client finds the calendar where it created it.
func (b *CalDAV) CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error {
	if err := b.allow(ctx, true); err != nil {
		return err
	}
	u, err := currentUser(ctx, b.Engine)
	if err != nil {
		return err
	}
	seg, file, err := parseHomePath(b.Prefix, calHomeSeg, calendar.Path, u)
	if err != nil {
		return err
	}
	if file != "" {
		return webdav.NewHTTPError(http.StatusForbidden, errors.New("calendar creation not allowed here"))
	}
	name := strings.TrimSpace(calendar.Name)
	if name == "" {
		name = seg
	}
	if !ValidDisplayName(name) {
		return webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid calendar name"))
	}
	if _, err := findHome(ctx, b.Engine, u, calKind, seg); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	if _, err := b.Engine.FindChildCollection(ctx, u.id, calKind, "", name); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	var meta json.RawMessage
	if !ids.Valid(seg) && seg != name {
		meta, _ = json.Marshal(colSlug{Slug: seg})
	}
	_, err = b.Engine.CreateCollection(ctx, u.id, calKind, name, "", meta)
	if errors.Is(err, syncengine.ErrCollectionExists) {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	return err
}

func (b *CalDAV) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	u, err := b.user(ctx)
	if err != nil {
		return nil, err
	}
	cols, err := listHome(ctx, b.Engine, u, calKind)
	if err != nil {
		return nil, err
	}
	base := homeBase(ctx, b.Prefix, u)
	out := make([]caldav.Calendar, 0, len(cols))
	for i := range cols {
		out = append(out, b.calInfo(base, &cols[i]))
	}
	return out, nil
}

func (b *CalDAV) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	u, hc, err := b.calendarByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	c := b.calInfo(homeBase(ctx, b.Prefix, u), hc)
	return &c, nil
}

func (b *CalDAV) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	_, obj, href, err := b.objectByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	return b.toCalendarObject(obj, href)
}

func (b *CalDAV) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	u, hc, err := b.calendarByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	objs, err := b.Engine.ListObjects(ctx, hc.col.ID)
	if err != nil {
		return nil, err
	}
	base := homeBase(ctx, b.Prefix, u)
	out := make([]caldav.CalendarObject, 0, len(objs))
	for i := range objs {
		href := Join(base, calHomeSeg, hc.seg, eventFileName(&objs[i]))
		co, err := b.toCalendarObject(&objs[i], href)
		if err != nil {
			continue
		}
		out = append(out, *co)
	}
	return out, nil
}

func (b *CalDAV) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	all, err := b.ListCalendarObjects(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	if query == nil {
		return all, nil
	}
	var out []caldav.CalendarObject
	for i := range all {
		ok, err := caldav.Match(query.CompFilter, &all[i])
		if err != nil {
			// Recurrence expansion can fail; keep the stored ICS rather than drop it.
			out = append(out, all[i])
			continue
		}
		if ok {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func (b *CalDAV) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	if err := b.allow(ctx, true); err != nil {
		return nil, err
	}
	u, err := currentUser(ctx, b.Engine)
	if err != nil {
		return nil, err
	}
	seg, fileName, err := parseHomePath(b.Prefix, calHomeSeg, path, u)
	if err != nil {
		return nil, err
	}
	if fileName == "" || !ValidDisplayName(fileName) {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid calendar object path"))
	}
	hc, err := findHome(ctx, b.Engine, u, calKind, seg)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusConflict, errors.New("calendar not found"))
	}
	if err := need(ctx, authz.CalDAVWrite, hc.access); err != nil {
		return nil, err
	}
	col := &hc.col
	comp, uid, err := caldav.ValidateCalendarObject(calendar)
	if err != nil || uid == "" {
		return nil, caldav.NewPreconditionError(caldav.PreconditionValidCalendarObjectResource)
	}
	switch comp {
	case ical.CompEvent, ical.CompToDo:
	default:
		return nil, caldav.NewPreconditionError(caldav.PreconditionSupportedCalendarComponent)
	}

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(calendar); err != nil {
		return nil, caldav.NewPreconditionError(caldav.PreconditionValidCalendarData)
	}
	payload := buf.Bytes()
	if int64(len(payload)) > b.Store.MaxBlob {
		return nil, caldav.NewPreconditionError(caldav.PreconditionMaxResourceSize)
	}

	byUID, uidErr := b.Engine.FindObjectByUID(ctx, col.ID, uid)
	if uidErr != nil && !errors.Is(uidErr, syncengine.ErrNotFound) {
		return nil, uidErr
	}
	existing, nameErr := b.Engine.FindObjectByName(ctx, col.ID, fileName)
	if nameErr != nil && !errors.Is(nameErr, syncengine.ErrNotFound) {
		return nil, nameErr
	}
	created := errors.Is(nameErr, syncengine.ErrNotFound)
	if !created && byUID != nil && byUID.ID != existing.ID {
		return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
	}
	if created && byUID != nil {
		return nil, caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)
	}

	if opts != nil {
		if created {
			if opts.IfMatch.IsSet() && !opts.IfMatch.IsWildcard() {
				return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("etag"))
			}
		} else {
			if opts.IfNoneMatch.IsSet() {
				ok, _ := opts.IfNoneMatch.MatchETag(existing.ContentHash)
				if opts.IfNoneMatch.IsWildcard() || ok {
					return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("exists"))
				}
			}
			if opts.IfMatch.IsSet() && !opts.IfMatch.IsWildcard() {
				ok, _ := opts.IfMatch.MatchETag(existing.ContentHash)
				if !ok {
					return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("etag"))
				}
			}
		}
	}

	sum := ncrypto.SHA256Hex(payload)
	blobID, size, err := b.Store.PutUserBlob(ctx, u.id, bytes.NewReader(payload), sum)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || int64(len(payload)) > b.Store.MaxBlob {
			return nil, caldav.NewPreconditionError(caldav.PreconditionMaxResourceSize)
		}
		return nil, err
	}

	dev := DeviceFrom(ctx)
	in := syncengine.ChangeInput{
		CollectionID: col.ID,
		Kind:         eventKind,
		ContentHash:  blobID,
		BlobID:       blobID,
		Size:         size,
		Metadata:     EncodeEventMeta(EventMeta{Name: fileName, UID: uid, Comp: comp}),
	}
	if created {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else {
		in.ObjectID = existing.ID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = existing.Revision
		in.Force = opts == nil || !opts.IfMatch.IsSet()
	}
	res, err := b.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return nil, err
	}
	if res[0].Status != "ok" {
		if res[0].Status == "conflict" {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("conflict"))
		}
		if res[0].Error == syncengine.ErrMirrorReadOnly {
			return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New(res[0].Error))
		}
		return nil, webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Error))
	}
	obj, err := b.Engine.GetObject(ctx, in.ObjectID)
	if err != nil {
		return nil, err
	}
	href := Join(homeBase(ctx, b.Prefix, u), calHomeSeg, hc.seg, fileName)
	return b.toCalendarObject(obj, href)
}

func (b *CalDAV) DeleteCalendarObject(ctx context.Context, path string) error {
	u, hc, file, err := b.resolve(ctx, path)
	if err != nil {
		return err
	}
	if file == "" {
		if err := need(ctx, authz.CollectionAdmin, hc.access); err != nil {
			return err
		}
		return b.deleteCalendar(ctx, &hc.col)
	}
	if err := need(ctx, authz.CalDAVWrite, hc.access); err != nil {
		return err
	}
	obj, err := b.findEventByName(ctx, &hc.col, file)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return err
	}
	res, err := b.Engine.Push(ctx, u.dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: hc.col.ID, Op: syncengine.OpDelete,
		BaseRevision: obj.Revision, Force: true,
	}})
	if err != nil {
		return err
	}
	if res[0].Status != "ok" {
		if res[0].Error == syncengine.ErrMirrorReadOnly {
			return webdav.NewHTTPError(http.StatusForbidden, errors.New(res[0].Error))
		}
		return webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
	}
	return nil
}

func (b *CalDAV) deleteCalendar(ctx context.Context, col *syncengine.Collection) error {
	dev := DeviceFrom(ctx)
	if ParseCalendarColMeta(col.Metadata).Mirror {
		// The owner may drop a whole mirrored calendar (checked by the
		// caller); its events are read-only to this device, so the server
		// deletes them. The Mac agent recreates it while it is still selected.
		dev = syncengine.LocalOperator()
	}
	objs, err := b.Engine.ListObjects(ctx, col.ID)
	if err != nil {
		return err
	}
	for i := range objs {
		o := objs[i]
		res, err := b.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
			ObjectID: o.ID, CollectionID: col.ID, Op: syncengine.OpDelete, BaseRevision: o.Revision, Force: true,
		}})
		if err != nil {
			return err
		}
		if res[0].Status != "ok" {
			return webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
		}
	}
	return b.Engine.TombstoneCollection(ctx, col.ID)
}

func (b *CalDAV) allow(ctx context.Context, write bool) error {
	act := authz.CalDAVRead
	if write {
		act = authz.CalDAVWrite
	}
	if err := authz.Allow(DeviceFrom(ctx), act); err != nil {
		return webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return nil
}

// resolve maps a path to its calendar (readable by the caller) and file name.
func (b *CalDAV) resolve(ctx context.Context, path string) (*davUser, *homeCol, string, error) {
	u, err := b.user(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	seg, file, err := parseHomePath(b.Prefix, calHomeSeg, path, u)
	if err != nil {
		return nil, nil, "", err
	}
	hc, err := findHome(ctx, b.Engine, u, calKind, seg)
	if err != nil {
		return nil, nil, "", err
	}
	if err := need(ctx, authz.CalDAVRead, hc.access); err != nil {
		return nil, nil, "", err
	}
	return u, hc, file, nil
}

func (b *CalDAV) calendarByPath(ctx context.Context, path string) (*davUser, *homeCol, error) {
	u, hc, file, err := b.resolve(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	if file != "" {
		return nil, nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	return u, hc, nil
}

func (b *CalDAV) findEventByName(ctx context.Context, col *syncengine.Collection, fileName string) (*syncengine.Object, error) {
	obj, err := b.Engine.FindObjectByName(ctx, col.ID, fileName)
	if err == nil {
		return obj, nil
	}
	if !errors.Is(err, syncengine.ErrNotFound) {
		return nil, err
	}
	objs, err := b.Engine.ListObjects(ctx, col.ID)
	if err != nil {
		return nil, err
	}
	for i := range objs {
		if eventFileName(&objs[i]) == fileName {
			return &objs[i], nil
		}
	}
	return nil, syncengine.ErrNotFound
}

func (b *CalDAV) objectByPath(ctx context.Context, path string) (*homeCol, *syncengine.Object, string, error) {
	u, hc, file, err := b.resolve(ctx, path)
	if err != nil {
		return nil, nil, "", err
	}
	if file == "" {
		return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	obj, err := b.findEventByName(ctx, &hc.col, file)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, nil, "", err
	}
	href := Join(homeBase(ctx, b.Prefix, u), calHomeSeg, hc.seg, file)
	return hc, obj, href, nil
}

func (b *CalDAV) calInfo(base string, hc *homeCol) caldav.Calendar {
	comps := []string{ical.CompEvent, ical.CompToDo}
	switch ParseCalendarColMeta(hc.col.Metadata).Comp {
	case ical.CompEvent:
		comps = []string{ical.CompEvent}
	case ical.CompToDo:
		comps = []string{ical.CompToDo}
	}
	return caldav.Calendar{
		Path:                  Join(base, calHomeSeg, hc.seg),
		Name:                  hc.displayName(),
		MaxResourceSize:       b.Store.MaxBlob,
		SupportedComponentSet: comps,
	}
}

func (b *CalDAV) toCalendarObject(obj *syncengine.Object, href string) (*caldav.CalendarObject, error) {
	var cal *ical.Calendar
	if obj.BlobID != "" {
		pt, err := b.Store.GetBlobPlaintext(obj.BlobID)
		if err != nil {
			return nil, err
		}
		cal, err = ical.NewDecoder(bytes.NewReader(pt)).Decode()
		if err != nil {
			return nil, err
		}
	} else {
		cal = ical.NewCalendar()
	}
	return &caldav.CalendarObject{
		Path:          href,
		ModTime:       syncengine.ModTime(obj.UpdatedAt),
		ContentLength: obj.Size,
		ETag:          obj.ContentHash,
		Data:          cal,
	}, nil
}

func eventFileName(o *syncengine.Object) string {
	m := ParseEventMeta(o.Metadata)
	n := m.Name
	if n == "" && m.UID != "" {
		n = m.UID + ".ics"
	}
	if n == "" {
		n = o.ID + ".ics"
	}
	return DAVResourceName(n, ".ics")
}

var _ caldav.Backend = (*CalDAV)(nil)
