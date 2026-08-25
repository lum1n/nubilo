package dav

import (
	"bytes"
	"context"
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
	cardUserSeg = "user"
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

func (b *CardDAV) CurrentUserPrincipal(ctx context.Context) (string, error) {
	if err := b.allow(ctx, false, ""); err != nil {
		return "", err
	}
	return b.Prefix + "/user/", nil
}

func (b *CardDAV) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	if err := b.allow(ctx, false, ""); err != nil {
		return "", err
	}
	return b.Prefix + "/user/addressbooks/", nil
}

func (b *CardDAV) CreateAddressBook(ctx context.Context, addressBook *carddav.AddressBook) error {
	if err := b.allow(ctx, true, ""); err != nil {
		return err
	}
	name := strings.TrimSpace(addressBook.Name)
	if name == "" {
		_, bookName, file, err := b.parseCardPath(addressBook.Path)
		if err != nil {
			return err
		}
		if file != "" || bookName == "" {
			return webdav.NewHTTPError(http.StatusForbidden, errors.New("address book creation not allowed here"))
		}
		name = bookName
	}
	if !ValidDisplayName(name) {
		return webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid address book name"))
	}
	if _, err := b.Engine.FindChildCollection(ctx, bookKind, "", name); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	_, err := b.Engine.CreateCollection(ctx, bookKind, name, "", nil)
	return err
}

func (b *CardDAV) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	if err := b.allow(ctx, false, ""); err != nil {
		return nil, err
	}
	cols, err := b.Engine.ChildCollections(ctx, bookKind, "")
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressBook, 0, len(cols))
	for i := range cols {
		if err := b.allow(ctx, false, cols[i].ID); err != nil {
			continue
		}
		out = append(out, b.bookInfo(&cols[i]))
	}
	return out, nil
}

func (b *CardDAV) GetAddressBook(ctx context.Context, path string) (*carddav.AddressBook, error) {
	col, err := b.bookByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := b.allow(ctx, false, col.ID); err != nil {
		return nil, err
	}
	ab := b.bookInfo(col)
	return &ab, nil
}

func (b *CardDAV) DeleteAddressBook(ctx context.Context, path string) error {
	col, err := b.bookByPath(ctx, path)
	if err != nil {
		return err
	}
	if err := b.allow(ctx, true, col.ID); err != nil {
		return err
	}
	return b.deleteBook(ctx, col)
}

func (b *CardDAV) GetAddressObject(ctx context.Context, path string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	col, obj, href, err := b.objectByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := b.allow(ctx, false, col.ID); err != nil {
		return nil, err
	}
	return b.toAddressObject(obj, href)
}

func (b *CardDAV) ListAddressObjects(ctx context.Context, path string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	col, err := b.bookByPath(ctx, path)
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
	out := make([]carddav.AddressObject, 0, len(objs))
	for i := range objs {
		href := Join(b.Prefix, cardUserSeg, cardHomeSeg, col.Name, contactFileName(&objs[i]))
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
	_, bookName, fileName, err := b.parseCardPath(path)
	if err != nil {
		return nil, err
	}
	if fileName == "" || !ValidDisplayName(fileName) {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid address object path"))
	}
	col, err := b.Engine.FindChildCollection(ctx, bookKind, "", bookName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, webdav.NewHTTPError(http.StatusConflict, errors.New("address book not found"))
		}
		return nil, err
	}
	if err := b.allow(ctx, true, col.ID); err != nil {
		return nil, err
	}
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
	blobID, size, err := b.Store.PutBlob(ctx, bytes.NewReader(payload), sum)
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
	href := Join(b.Prefix, cardUserSeg, cardHomeSeg, col.Name, fileName)
	return b.toAddressObject(obj, href)
}

func (b *CardDAV) DeleteAddressObject(ctx context.Context, path string) error {
	_, bookName, fileName, err := b.parseCardPath(path)
	if err != nil {
		return err
	}
	if fileName == "" {
		return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	col, err := b.Engine.FindChildCollection(ctx, bookKind, "", bookName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return err
	}
	if err := b.allow(ctx, true, col.ID); err != nil {
		return err
	}
	obj, err := b.Engine.FindObjectByName(ctx, col.ID, fileName)
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

func (b *CardDAV) allow(ctx context.Context, write bool, collectionID string) error {
	dev := DeviceFrom(ctx)
	act := authz.CardDAVRead
	if write {
		act = authz.CardDAVWrite
	}
	if err := authz.Allow(dev, act, collectionID); err != nil {
		return webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return nil
}

func (b *CardDAV) bookByPath(ctx context.Context, path string) (*syncengine.Collection, error) {
	_, bookName, file, err := b.parseCardPath(path)
	if err != nil {
		return nil, err
	}
	if file != "" || bookName == "" {
		return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	col, err := b.Engine.FindChildCollection(ctx, bookKind, "", bookName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, err
	}
	return col, nil
}

func (b *CardDAV) objectByPath(ctx context.Context, path string) (*syncengine.Collection, *syncengine.Object, string, error) {
	_, bookName, fileName, err := b.parseCardPath(path)
	if err != nil {
		return nil, nil, "", err
	}
	if fileName == "" {
		return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	col, err := b.Engine.FindChildCollection(ctx, bookKind, "", bookName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, nil, "", err
	}
	obj, err := b.Engine.FindObjectByName(ctx, col.ID, fileName)
	if err != nil {
		if errors.Is(err, syncengine.ErrNotFound) {
			return nil, nil, "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		return nil, nil, "", err
	}
	href := Join(b.Prefix, cardUserSeg, cardHomeSeg, col.Name, fileName)
	return col, obj, href, nil
}

func (b *CardDAV) parseCardPath(p string) (segs []string, bookName, fileName string, err error) {
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
	if len(segs) < 3 || segs[0] != cardUserSeg || segs[1] != cardHomeSeg {
		return segs, "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	if !ValidDisplayName(segs[2]) {
		return segs, "", "", webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid address book name"))
	}
	bookName = segs[2]
	if len(segs) == 3 {
		return segs, bookName, "", nil
	}
	if len(segs) == 4 {
		if !ValidDisplayName(segs[3]) {
			return segs, "", "", webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid object name"))
		}
		return segs, bookName, segs[3], nil
	}
	return segs, "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
}

func (b *CardDAV) bookInfo(c *syncengine.Collection) carddav.AddressBook {
	return carddav.AddressBook{
		Path:            Join(b.Prefix, cardUserSeg, cardHomeSeg, c.Name),
		Name:            c.Name,
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
