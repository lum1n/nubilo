package dav

import (
	"bytes"
	"context"
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
	calUserSeg = "user"
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

func (b *CalDAV) CurrentUserPrincipal(ctx context.Context) (string, error) {
	if err := b.allow(ctx, false, ""); err != nil {
		return "", err
	}
	return b.Prefix + "/user/", nil
}

func (b *CalDAV) CalendarHomeSetPath(ctx context.Context) (string, error) {
	if err := b.allow(ctx, false, ""); err != nil {
		return "", err
	}
	return b.Prefix + "/user/calendars/", nil
}

func (b *CalDAV) CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error {
	if err := b.allow(ctx, true, ""); err != nil {
		return err
	}
	name := strings.TrimSpace(calendar.Name)
	if name == "" {
		_, calName, file, err := b.parseCalPath(calendar.Path)
		if err != nil {
			return err
		}
		if file != "" || calName == "" {
			return webdav.NewHTTPError(http.StatusForbidden, errors.New("calendar creation not allowed here"))
		}
		name = calName
	}
	if !ValidDisplayName(name) {
		return webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid calendar name"))
	}
	if _, err := b.Engine.FindChildCollection(ctx, calKind, "", name); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	_, err := b.Engine.CreateCollection(ctx, calKind, name, "", nil)
	return err
}

func (b *CalDAV) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	if err := b.allow(ctx, false, ""); err != nil {
		return nil, err
	}
	cols, err := b.Engine.ChildCollections(ctx, calKind, "")
	if err != nil {
		return nil, err
	}
	out := make([]caldav.Calendar, 0, len(cols))
	for i := range cols {
		if err := b.allow(ctx, false, cols[i].ID); err != nil {
			continue
		}
		out = append(out, b.calInfo(&cols[i]))
	}
	return out, nil
}

func (b *CalDAV) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	col, err := b.calendarByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := b.allow(ctx, false, col.ID); err != nil {
		return nil, err
	}
	c := b.calInfo(col)
	return &c, nil
}

func (b *CalDAV) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	col, obj, href, err := b.objectByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := b.allow(ctx, false, col.ID); err != nil {
		return nil, err
	}
	return b.toCalendarObject(obj, href)
}

func (b *CalDAV) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	col, err := b.calendarByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := b.allow(ctx, false, col.ID); err != nil {
		return nil, err
	}
	objs, err := b.Engine.ListObjects(ctx, col.ID)
	if err != nil {
		return nil, err
	}
	out := make([]caldav.CalendarObject, 0, len(objs))
	for i := range objs {
		href := Join(b.Prefix, calUserSeg, calHomeSeg, col.Name, eventFileName(&objs[i]))
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
	_, calName, fileName, err := b.parseCalPath(path)
	if err != nil {
		return nil, err
	}
	if fileName == "" || !ValidDisplayName(fileName) {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid calendar object path"))
	}
	col, err := b.Engine.FindChildCollection(ctx, calKind, "", calName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, webdav.NewHTTPError(http.StatusConflict, errors.New("calendar not found"))
		}
		return nil, err
	}
	if err := b.allow(ctx, true, col.ID); err != nil {
		return nil, err
	}
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
	blobID, size, err := b.Store.PutBlob(ctx, bytes.NewReader(payload), sum)
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
		return nil, webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Error))
	}
	obj, err := b.Engine.GetObject(ctx, in.ObjectID)
	if err != nil {
		return nil, err
	}
	href := Join(b.Prefix, calUserSeg, calHomeSeg, col.Name, fileName)
	return b.toCalendarObject(obj, href)
}

func (b *CalDAV) DeleteCalendarObject(ctx context.Context, path string) error {
	_, calName, fileName, err := b.parseCalPath(path)
	if err != nil {
		return err
	}
	col, err := b.Engine.FindChildCollection(ctx, calKind, "", calName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return err
	}
	if err := b.allow(ctx, true, col.ID); err != nil {
		return err
	}
	if fileName == "" {
		return b.deleteCalendar(ctx, col)
	}
	obj, err := b.findEventByName(ctx, col, fileName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return err
	}
	dev := DeviceFrom(ctx)
	res, err := b.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: col.ID, Op: syncengine.OpDelete,
		BaseRevision: obj.Revision, Force: true,
	}})
	if err != nil {
		return err
	}
	if res[0].Status != "ok" {
		return webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
	}
	return nil
}

func (b *CalDAV) deleteCalendar(ctx context.Context, col *syncengine.Collection) error {
	dev := DeviceFrom(ctx)
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

func (b *CalDAV) allow(ctx context.Context, write bool, collectionID string) error {
	dev := DeviceFrom(ctx)
	act := authz.CalDAVRead
	if write {
		act = authz.CalDAVWrite
	}
	if err := authz.Allow(dev, act, collectionID); err != nil {
		return webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return nil
}

func (b *CalDAV) calendarByPath(ctx context.Context, path string) (*syncengine.Collection, error) {
	_, calName, file, err := b.parseCalPath(path)
	if err != nil {
		return nil, err
	}
	if file != "" || calName == "" {
		return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	col, err := b.Engine.FindChildCollection(ctx, calKind, "", calName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, err
	}
	return col, nil
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

func (b *CalDAV) objectByPath(ctx context.Context, path string) (*syncengine.Collection, *syncengine.Object, string, error) {
	_, calName, fileName, err := b.parseCalPath(path)
	if err != nil {
		return nil, nil, "", err
	}
	if fileName == "" {
		return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	col, err := b.Engine.FindChildCollection(ctx, calKind, "", calName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, nil, "", err
	}
	obj, err := b.findEventByName(ctx, col, fileName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, nil, "", err
	}
	href := Join(b.Prefix, calUserSeg, calHomeSeg, col.Name, fileName)
	return col, obj, href, nil
}

func (b *CalDAV) parseCalPath(p string) (segs []string, calName, fileName string, err error) {
	n, err := Normalize(p)
	if err != nil {
		return nil, "", "", webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if n != b.Prefix && !HasPrefix(n, b.Prefix) {
		return nil, "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	rel := strings.TrimPrefix(n, b.Prefix)
	segs, err = Split(rel)
	if err != nil {
		return nil, "", "", webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if len(segs) < 3 || segs[0] != calUserSeg || segs[1] != calHomeSeg {
		return segs, "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	if !ValidDisplayName(segs[2]) {
		return segs, "", "", webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid calendar name"))
	}
	calName = segs[2]
	if len(segs) == 3 {
		return segs, calName, "", nil
	}
	if len(segs) == 4 {
		if !ValidDisplayName(segs[3]) {
			return segs, "", "", webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid object name"))
		}
		return segs, calName, segs[3], nil
	}
	return segs, "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
}

func (b *CalDAV) calInfo(c *syncengine.Collection) caldav.Calendar {
	return caldav.Calendar{
		Path:                  Join(b.Prefix, calUserSeg, calHomeSeg, c.Name),
		Name:                  c.Name,
		MaxResourceSize:       b.Store.MaxBlob,
		SupportedComponentSet: []string{ical.CompEvent, ical.CompToDo},
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
