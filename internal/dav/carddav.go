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

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"nubilo/internal/authz"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

const CardDAVPrefix = "/carddav"

const (
	bookKind    = "addressbook"
	contactKind = "contact"
	cardHomeSeg = "addressbooks"
)

type CardDAV struct {
	Engine *syncengine.Engine
	Store  *store.Store
	Prefix string
}

func NewCardDAV(eng *syncengine.Engine, st *store.Store) *CardDAV {
	return &CardDAV{Engine: eng, Store: st, Prefix: CardDAVPrefix}
}

func (b *CardDAV) user(ctx context.Context) (*davUser, error) {
	if err := b.allow(ctx, false); err != nil {
		return nil, err
	}
	return currentUser(ctx, b.Engine)
}

func (b *CardDAV) CurrentUserPrincipal(ctx context.Context) (string, error) {
	u, err := b.user(ctx)
	if err != nil {
		return "", err
	}
	return homeBase(ctx, b.Prefix, u) + "/", nil
}

func (b *CardDAV) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	u, err := b.user(ctx)
	if err != nil {
		return "", err
	}
	return homeBase(ctx, b.Prefix, u) + "/" + cardHomeSeg + "/", nil
}

func (b *CardDAV) CreateAddressBook(ctx context.Context, addressBook *carddav.AddressBook) error {
	if err := b.allow(ctx, true); err != nil {
		return err
	}
	u, err := currentUser(ctx, b.Engine)
	if err != nil {
		return err
	}
	seg, file, err := parseHomePath(b.Prefix, cardHomeSeg, addressBook.Path, u)
	if err != nil {
		return err
	}
	if file != "" {
		return webdav.NewHTTPError(http.StatusForbidden, errors.New("address book creation not allowed here"))
	}
	name := strings.TrimSpace(addressBook.Name)
	if name == "" {
		name = seg
	}
	if !ValidDisplayName(name) {
		return webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid address book name"))
	}
	if _, err := findHome(ctx, b.Engine, u, bookKind, seg); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	if _, err := b.Engine.FindChildCollection(ctx, u.id, bookKind, "", name); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	var meta json.RawMessage
	if !ids.Valid(seg) && seg != name {
		meta, _ = json.Marshal(colSlug{Slug: seg})
	}
	_, err = b.Engine.CreateCollection(ctx, u.id, bookKind, name, "", meta)
	if errors.Is(err, syncengine.ErrCollectionExists) {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	return err
}

func (b *CardDAV) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	u, err := b.user(ctx)
	if err != nil {
		return nil, err
	}
	cols, err := listHome(ctx, b.Engine, u, bookKind)
	if err != nil {
		return nil, err
	}
	base := homeBase(ctx, b.Prefix, u)
	out := make([]carddav.AddressBook, 0, len(cols))
	for i := range cols {
		out = append(out, b.bookInfo(base, &cols[i]))
	}
	return out, nil
}

func (b *CardDAV) GetAddressBook(ctx context.Context, path string) (*carddav.AddressBook, error) {
	u, hc, err := b.bookByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	ab := b.bookInfo(homeBase(ctx, b.Prefix, u), hc)
	return &ab, nil
}

func (b *CardDAV) DeleteAddressBook(ctx context.Context, path string) error {
	_, hc, err := b.bookByPath(ctx, path)
	if err != nil {
		return err
	}
	if err := need(ctx, authz.CollectionAdmin, hc.access); err != nil {
		return err
	}
	return b.deleteBook(ctx, &hc.col)
}

func (b *CardDAV) GetAddressObject(ctx context.Context, path string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	_, obj, href, err := b.objectByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	return b.toAddressObject(obj, href)
}

func (b *CardDAV) ListAddressObjects(ctx context.Context, path string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	u, hc, err := b.bookByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	objs, err := b.Engine.ListObjects(ctx, hc.col.ID)
	if err != nil {
		return nil, err
	}
	base := homeBase(ctx, b.Prefix, u)
	out := make([]carddav.AddressObject, 0, len(objs))
	for i := range objs {
		href := Join(base, cardHomeSeg, hc.seg, contactFileName(&objs[i]))
		ao, err := b.toAddressObject(&objs[i], href)
		if err != nil {
			continue
		}
		out = append(out, *ao)
	}
	return out, nil
}

func (b *CardDAV) QueryAddressObjects(ctx context.Context, path string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	all, err := b.ListAddressObjects(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	if query == nil || len(query.PropFilters) == 0 {
		if query != nil && query.Limit > 0 && query.Limit < len(all) {
			return all[:query.Limit], nil
		}
		return all, nil
	}
	out, err := carddav.Filter(query, all)
	if err != nil {
		return all, nil
	}
	return out, nil
}

func (b *CardDAV) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	if err := b.allow(ctx, true); err != nil {
		return nil, err
	}
	u, err := currentUser(ctx, b.Engine)
	if err != nil {
		return nil, err
	}
	seg, fileName, err := parseHomePath(b.Prefix, cardHomeSeg, path, u)
	if err != nil {
		return nil, err
	}
	if fileName == "" || !ValidDisplayName(fileName) {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid address object path"))
	}
	hc, err := findHome(ctx, b.Engine, u, bookKind, seg)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusConflict, errors.New("address book not found"))
	}
	if err := need(ctx, authz.CardDAVWrite, hc.access); err != nil {
		return nil, err
	}
	col := &hc.col
	uid := strings.TrimSpace(card.Value(vcard.FieldUID))
	if uid == "" {
		return nil, carddav.NewPreconditionError(carddav.PreconditionValidAddressData)
	}

	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, carddav.NewPreconditionError(carddav.PreconditionValidAddressData)
	}
	payload := buf.Bytes()
	if int64(len(payload)) > b.Store.MaxBlob {
		return nil, carddav.NewPreconditionError(carddav.PreconditionMaxResourceSize)
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
		return nil, carddav.NewPreconditionError(carddav.PreconditionNoUIDConflict)
	}
	if created && byUID != nil {
		return nil, carddav.NewPreconditionError(carddav.PreconditionNoUIDConflict)
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
			return nil, carddav.NewPreconditionError(carddav.PreconditionMaxResourceSize)
		}
		return nil, err
	}

	dev := DeviceFrom(ctx)
	in := syncengine.ChangeInput{
		CollectionID: col.ID,
		Kind:         contactKind,
		ContentHash:  blobID,
		BlobID:       blobID,
		Size:         size,
		Metadata:     EncodeContactMeta(ContactMetaFromVCard(fileName, uid, payload)),
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
	href := Join(homeBase(ctx, b.Prefix, u), cardHomeSeg, hc.seg, fileName)
	return b.toAddressObject(obj, href)
}

func (b *CardDAV) DeleteAddressObject(ctx context.Context, path string) error {
	u, hc, file, err := b.resolve(ctx, path)
	if err != nil {
		return err
	}
	if file == "" {
		return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	if err := need(ctx, authz.CardDAVWrite, hc.access); err != nil {
		return err
	}
	obj, err := b.Engine.FindObjectByName(ctx, hc.col.ID, file)
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
		return webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
	}
	return nil
}

func (b *CardDAV) deleteBook(ctx context.Context, col *syncengine.Collection) error {
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

func (b *CardDAV) allow(ctx context.Context, write bool) error {
	act := authz.CardDAVRead
	if write {
		act = authz.CardDAVWrite
	}
	if err := authz.Allow(DeviceFrom(ctx), act); err != nil {
		return webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return nil
}

func (b *CardDAV) resolve(ctx context.Context, path string) (*davUser, *homeCol, string, error) {
	u, err := b.user(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	seg, file, err := parseHomePath(b.Prefix, cardHomeSeg, path, u)
	if err != nil {
		return nil, nil, "", err
	}
	hc, err := findHome(ctx, b.Engine, u, bookKind, seg)
	if err != nil {
		return nil, nil, "", err
	}
	if err := need(ctx, authz.CardDAVRead, hc.access); err != nil {
		return nil, nil, "", err
	}
	return u, hc, file, nil
}

func (b *CardDAV) bookByPath(ctx context.Context, path string) (*davUser, *homeCol, error) {
	u, hc, file, err := b.resolve(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	if file != "" {
		return nil, nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	return u, hc, nil
}

func (b *CardDAV) objectByPath(ctx context.Context, path string) (*homeCol, *syncengine.Object, string, error) {
	u, hc, file, err := b.resolve(ctx, path)
	if err != nil {
		return nil, nil, "", err
	}
	if file == "" {
		return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	obj, err := b.Engine.FindObjectByName(ctx, hc.col.ID, file)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, nil, "", err
	}
	href := Join(homeBase(ctx, b.Prefix, u), cardHomeSeg, hc.seg, file)
	return hc, obj, href, nil
}

func (b *CardDAV) bookInfo(base string, hc *homeCol) carddav.AddressBook {
	return carddav.AddressBook{
		Path:            Join(base, cardHomeSeg, hc.seg),
		Name:            hc.displayName(),
		MaxResourceSize: b.Store.MaxBlob,
		SupportedAddressData: []carddav.AddressDataType{
			{ContentType: vcard.MIMEType, Version: "3.0"},
			{ContentType: vcard.MIMEType, Version: "4.0"},
		},
	}
}

func (b *CardDAV) toAddressObject(obj *syncengine.Object, href string) (*carddav.AddressObject, error) {
	var card vcard.Card
	if obj.BlobID != "" {
		pt, err := b.Store.GetBlobPlaintext(obj.BlobID)
		if err != nil {
			return nil, err
		}
		card, err = vcard.NewDecoder(bytes.NewReader(pt)).Decode()
		if err != nil {
			return nil, err
		}
	} else {
		card = vcard.Card{}
	}
	return &carddav.AddressObject{
		Path:          href,
		ModTime:       syncengine.ModTime(obj.UpdatedAt),
		ContentLength: obj.Size,
		ETag:          obj.ContentHash,
		Card:          card,
	}, nil
}

func contactFileName(o *syncengine.Object) string {
	m := ParseContactMeta(o.Metadata)
	n := m.Name
	if n == "" && m.UID != "" {
		n = m.UID + ".vcf"
	}
	if n == "" {
		n = o.ID + ".vcf"
	}
	return DAVResourceName(n, ".vcf")
}

var _ carddav.Backend = (*CardDAV)(nil)
