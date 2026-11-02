package photos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"strings"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

// Client-made derivatives. The server decodes only the formats the Go
// standard library and x/image know, so HEIC photos and videos from Apple
// devices get no thumbnail or preview here. The apps make JPEGs from the
// original they already have and upload them; these limits keep a client
// from storing anything else under a photo's thumbnail.
const (
	MaxThumbSide         = 512
	MaxPreviewSide       = 2048
	MaxDerivativeBytes   = 4 << 20
	derivativeThumbKey   = "thumb_hash"
	derivativePreviewKey = "preview_hash"
)

var ErrBadDerivative = errors.New("photos: derivative must be a JPEG within the size limits")

// ValidDerivative checks that b is a JPEG whose long side is at most maxSide.
func ValidDerivative(b []byte, maxSide int) error {
	if len(b) == 0 || len(b) > MaxDerivativeBytes {
		return ErrBadDerivative
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "jpeg" {
		return ErrBadDerivative
	}
	if cfg.Width < 1 || cfg.Height < 1 || max(cfg.Width, cfg.Height) > maxSide {
		return ErrBadDerivative
	}
	return nil
}

// clientDerivative loads an uploaded derivative blob the user holds a
// reference to, and checks it.
func (s Service) clientDerivative(ctx context.Context, userID, hash string, maxSide int) ([]byte, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if !store.IsBlobID(hash) {
		return nil, ErrBadDerivative
	}
	ok, err := s.Store.HasBlobRef(ctx, userID, hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrForbidden
	}
	b, err := s.Store.GetBlobPlaintext(hash)
	if err != nil {
		return nil, err
	}
	if err := ValidDerivative(b, maxSide); err != nil {
		return nil, err
	}
	return b, nil
}

// AttachDerivatives sets a library photo's thumbnail and preview from blobs
// the device's user uploaded, where the photo has none yet. Derivatives the
// server made are never replaced. It returns the photo as it is afterwards.
func (s Service) AttachDerivatives(ctx context.Context, dev *identity.Device, photoID, thumbBlob, previewBlob string) (*syncengine.Object, error) {
	if dev == nil || dev.UserID == "" {
		return nil, ErrNoUser
	}
	obj, err := s.Get(ctx, dev, photoID)
	if err != nil {
		return nil, err
	}
	if obj.Kind != ObjectKind {
		return nil, ErrNotPhoto
	}
	col, err := s.Library(ctx, dev.UserID)
	if err != nil {
		return nil, err
	}
	if obj.CollectionID != col.ID {
		// Someone else's photo, seen through a share.
		return nil, ErrForbidden
	}
	m := ParseMeta(obj.Metadata)
	set := map[string]string{}
	if thumbBlob != "" && m.ThumbHash == "" {
		if _, err := s.clientDerivative(ctx, dev.UserID, thumbBlob, MaxThumbSide); err != nil {
			return nil, err
		}
		set[derivativeThumbKey] = strings.ToLower(thumbBlob)
	}
	if previewBlob != "" && m.PreviewHash == "" {
		if _, err := s.clientDerivative(ctx, dev.UserID, previewBlob, MaxPreviewSide); err != nil {
			return nil, err
		}
		set[derivativePreviewKey] = strings.ToLower(previewBlob)
	}
	if len(set) == 0 {
		return obj, nil
	}
	meta, err := syncengine.MergeJSON(obj.Metadata, mustJSON(set))
	if err != nil {
		return nil, err
	}
	res, err := s.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID:     obj.ID,
		CollectionID: obj.CollectionID,
		Kind:         ObjectKind,
		Op:           syncengine.OpUpdate,
		BaseRevision: obj.Revision,
		ContentHash:  obj.ContentHash,
		BlobID:       obj.BlobID,
		Size:         obj.Size,
		Metadata:     meta,
	}})
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		msg := "update rejected"
		if len(res) > 0 {
			msg = res[0].Status + " " + res[0].Error
		}
		return nil, fmt.Errorf("photos: %s", strings.TrimSpace(msg))
	}
	return s.Engine.GetObject(ctx, obj.ID)
}

// WithSourceDerivatives fills an album item's missing thumbnail and preview
// from the library photo it was added from: derivatives attached after the
// item was made live only on the photo. Anything else comes back unchanged.
func (s Service) WithSourceDerivatives(ctx context.Context, item *syncengine.Object) *syncengine.Object {
	if item.Kind != AlbumItemKind {
		return item
	}
	m := ParseMeta(item.Metadata)
	if m.ThumbHash != "" && m.PreviewHash != "" {
		return item
	}
	src, err := s.Engine.GetObject(ctx, AlbumItemPhotoID(item.Metadata))
	if err != nil || src.DeletedAt != nil || src.Kind != ObjectKind || src.ContentHash != item.ContentHash {
		return item
	}
	sm := ParseMeta(src.Metadata)
	set := map[string]string{}
	if m.ThumbHash == "" && sm.ThumbHash != "" {
		set[derivativeThumbKey] = sm.ThumbHash
	}
	if m.PreviewHash == "" && sm.PreviewHash != "" {
		set[derivativePreviewKey] = sm.PreviewHash
	}
	if len(set) == 0 {
		return item
	}
	meta, err := syncengine.MergeJSON(item.Metadata, mustJSON(set))
	if err != nil {
		return item
	}
	out := *item
	out.Metadata = meta
	return &out
}

// IndexEntry is one photo in a library or album index: what a gallery needs
// to lay out its grid and fetch thumbnails, and nothing more.
type IndexEntry struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Kind         string `json:"kind,omitempty"`
	MIME         string `json:"mime,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	TakenAtMS    int64  `json:"taken_at_ms,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Checksum     string `json:"checksum,omitempty"`
	ThumbHash    string `json:"thumb_hash,omitempty"`
	PreviewHash  string `json:"preview_hash,omitempty"`
	LivePairHash string `json:"live_pair_hash,omitempty"`
	PhotoID      string `json:"photo_id,omitempty"`
}

func indexEntry(o *syncengine.Object) IndexEntry {
	m := ParseMeta(o.Metadata)
	e := IndexEntry{
		ID: o.ID, Name: m.Name, Kind: m.Kind, MIME: m.MIME, Width: m.Width, Height: m.Height,
		TakenAtMS: m.TakenAtMS, Size: o.Size, Checksum: m.Checksum, ThumbHash: m.ThumbHash,
		PreviewHash: m.PreviewHash, LivePairHash: m.LivePairHash,
	}
	if o.Kind == AlbumItemKind {
		e.PhotoID = AlbumItemPhotoID(o.Metadata)
	}
	return e
}

// LibraryIndex lists every photo in userID's library.
func (s Service) LibraryIndex(ctx context.Context, userID string) ([]IndexEntry, error) {
	col, err := s.Library(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.collectionIndex(ctx, col.ID, ObjectKind)
}

// AlbumIndex lists every item of an album the device may read.
func (s Service) AlbumIndex(ctx context.Context, dev *identity.Device, albumID string) ([]IndexEntry, error) {
	if _, err := s.album(ctx, dev, albumID, authz.AccessRead); err != nil {
		return nil, err
	}
	return s.collectionIndex(ctx, albumID, AlbumItemKind)
}

func (s Service) collectionIndex(ctx context.Context, collectionID, kind string) ([]IndexEntry, error) {
	out := []IndexEntry{}
	after := ""
	for {
		objs, next, err := s.ListCollection(ctx, collectionID, kind, after, 1000)
		if err != nil {
			return nil, err
		}
		for i := range objs {
			out = append(out, indexEntry(s.WithSourceDerivatives(ctx, &objs[i])))
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
}
