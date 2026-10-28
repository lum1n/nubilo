package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"nubilo/internal/authz"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

var (
	ErrNotPhoto      = errors.New("photos: not a photo object")
	ErrQuotaExceeded = errors.New("photos: quota exceeded")
	ErrNoUser        = errors.New("photos: device has no user")
	ErrForbidden     = errors.New("photos: forbidden")
)

type Service struct {
	Engine *syncengine.Engine
	Store  *store.Store
	Opt    Options
}

func (s Service) options() Options {
	o := DefaultOptions()
	if s.Opt.ThumbMaxPx > 0 {
		o.ThumbMaxPx = s.Opt.ThumbMaxPx
	}
	if s.Opt.PreviewMaxPx > 0 {
		o.PreviewMaxPx = s.Opt.PreviewMaxPx
	}
	o.StripGPSFromDerivatives = s.Opt.StripGPSFromDerivatives
	o.PerceptualHash = s.Opt.PerceptualHash
	return o
}

// Library returns (creating if needed) the photo library of userID.
func (s Service) Library(ctx context.Context, userID string) (*syncengine.Collection, error) {
	if userID == "" {
		return nil, ErrNoUser
	}
	return s.Engine.EnsureNamedCollection(ctx, userID, CollectionKind, DefaultName)
}

// Ingest stores an original, generates derivatives and adds the photo to the
// device user's library.
func (s Service) Ingest(ctx context.Context, dev *identity.Device, original []byte, name string, albums []string) (*syncengine.Object, error) {
	if len(original) == 0 {
		return nil, fmt.Errorf("photos: empty body")
	}
	if dev == nil || dev.UserID == "" {
		return nil, ErrNoUser
	}
	prep, err := Prepare(original, name, s.options())
	if err != nil {
		return nil, err
	}
	return s.ingestPrepared(ctx, dev, prep, albums, "")
}

// CommitRequest adds a photo whose original was already uploaded as a blob.
type CommitRequest struct {
	BlobID        string
	Name          string
	TakenAtMS     int64
	LocalID       string
	LiveVideoBlob string
}

// Commit ingests an already-uploaded blob (PUT /sync/v1/blob) into the
// device user's library. The user must hold references to the blobs.
func (s Service) Commit(ctx context.Context, dev *identity.Device, req CommitRequest) (*syncengine.Object, error) {
	if dev == nil || dev.UserID == "" {
		return nil, ErrNoUser
	}
	for _, h := range []string{req.BlobID, req.LiveVideoBlob} {
		if h == "" {
			continue
		}
		ok, err := s.Store.HasBlobRef(ctx, dev.UserID, h)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrForbidden
		}
	}
	original, err := s.Store.GetBlobPlaintext(req.BlobID)
	if err != nil {
		return nil, err
	}
	prep, err := Prepare(original, req.Name, s.options())
	if err != nil {
		return nil, err
	}
	if req.TakenAtMS > 0 && prep.Meta.TakenAtMS == 0 {
		prep.Meta.TakenAtMS = req.TakenAtMS
	}
	if req.LiveVideoBlob != "" {
		prep.Meta.LivePairHash = req.LiveVideoBlob
		if prep.Meta.Kind == "image" || prep.Meta.Kind == "" {
			prep.Meta.Kind = "live"
		}
	}
	return s.ingestPrepared(ctx, dev, prep, nil, req.LocalID)
}

func (s Service) ingestPrepared(ctx context.Context, dev *identity.Device, prep Prepared, albums []string, localID string) (*syncengine.Object, error) {
	origHash := ncrypto.SHA256Hex(prep.Original)
	if _, _, err := s.Store.PutUserBlob(ctx, dev.UserID, bytes.NewReader(prep.Original), origHash); err != nil {
		return nil, err
	}
	prep.Meta.Checksum = origHash
	if len(prep.Preview) > 0 {
		ph := ncrypto.SHA256Hex(prep.Preview)
		if _, _, err := s.Store.PutUserBlob(ctx, dev.UserID, bytes.NewReader(prep.Preview), ph); err != nil {
			return nil, err
		}
		prep.Meta.PreviewHash = ph
	}
	if len(prep.Thumb) > 0 {
		th := ncrypto.SHA256Hex(prep.Thumb)
		if _, _, err := s.Store.PutUserBlob(ctx, dev.UserID, bytes.NewReader(prep.Thumb), th); err != nil {
			return nil, err
		}
		prep.Meta.ThumbHash = th
	}
	prep.Meta.Albums = albums
	col, err := s.Library(ctx, dev.UserID)
	if err != nil {
		return nil, err
	}
	if existing, err := s.Engine.FindObjectByName(ctx, col.ID, prep.Meta.Name); err == nil && existing.ContentHash == origHash {
		return existing, nil
	}
	if existing, err := s.findByChecksum(ctx, col.ID, origHash); err == nil {
		return existing, nil
	}
	meta := EncodeMeta(prep.Meta)
	if localID != "" {
		if m, err := syncengine.MergeJSON(meta, mustJSON(map[string]string{"local_id": localID})); err == nil {
			meta = m
		}
	}
	res, err := s.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID:     ids.New(),
		CollectionID: col.ID,
		Kind:         ObjectKind,
		Op:           syncengine.OpCreate,
		ContentHash:  origHash,
		BlobID:       origHash,
		Size:         int64(len(prep.Original)),
		Metadata:     meta,
		Force:        true,
	}})
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		msg := "push rejected"
		if len(res) > 0 && res[0].Error != "" {
			msg = res[0].Error
		}
		if msg == syncengine.ErrQuotaExceeded {
			return nil, ErrQuotaExceeded
		}
		return nil, fmt.Errorf("photos: %s", msg)
	}
	return s.Engine.GetObject(ctx, res[0].ObjectID)
}

func (s Service) findByChecksum(ctx context.Context, collectionID, hash string) (*syncengine.Object, error) {
	return s.Engine.FindLiveObjectByBlob(ctx, collectionID, hash)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Missing returns the hashes in want that userID's library does not hold yet.
func (s Service) Missing(ctx context.Context, userID string, want []string) ([]string, error) {
	col, err := s.Library(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, h := range want {
		h = strings.ToLower(strings.TrimSpace(h))
		if !store.IsBlobID(h) {
			continue
		}
		if _, err := s.Engine.FindLiveObjectByBlob(ctx, col.ID, h); errors.Is(err, syncengine.ErrNotFound) {
			out = append(out, h)
		} else if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// List pages through userID's library in id order. next is empty on the last page.
func (s Service) List(ctx context.Context, userID, after string, limit int) (objs []syncengine.Object, next string, err error) {
	col, err := s.Library(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	return s.ListCollection(ctx, col.ID, ObjectKind, after, limit)
}

// ListCollection pages live objects of kind in one collection.
func (s Service) ListCollection(ctx context.Context, collectionID, kind, after string, limit int) ([]syncengine.Object, string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	objs, err := s.Engine.ListObjectsPage(ctx, collectionID, kind, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(objs) > limit {
		objs = objs[:limit]
		next = objs[limit-1].ID
	}
	return objs, next, nil
}

// Get returns a photo or album item the device may read.
func (s Service) Get(ctx context.Context, dev *identity.Device, id string) (*syncengine.Object, error) {
	obj, err := s.Engine.GetObject(ctx, id)
	if err != nil {
		return nil, err
	}
	if obj.DeletedAt != nil {
		return nil, syncengine.ErrNotFound
	}
	if obj.Kind != ObjectKind && obj.Kind != AlbumItemKind {
		return nil, ErrNotPhoto
	}
	access, err := s.Engine.AccessFor(ctx, dev, obj.CollectionID)
	if err != nil {
		return nil, err
	}
	if access < authz.AccessRead {
		// Same answer as a missing photo: do not reveal other users' ids.
		return nil, syncengine.ErrNotFound
	}
	return obj, nil
}

func (s Service) Blob(obj *syncengine.Object, which string) (mime string, body []byte, err error) {
	m := ParseMeta(obj.Metadata)
	hash := obj.BlobID
	mime = m.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	switch strings.ToLower(which) {
	case "", "original":
		hash = obj.BlobID
	case "preview":
		hash = m.PreviewHash
		mime = "image/jpeg"
		if hash == "" {
			hash = obj.BlobID
			mime = m.MIME
		}
	case "thumb", "thumbnail":
		hash = m.ThumbHash
		mime = "image/jpeg"
		if hash == "" {
			hash = obj.BlobID
			mime = m.MIME
		}
	case "live", "livepair", "live_movie":
		hash = m.LivePairHash
		mime = "video/quicktime"
	default:
		return "", nil, fmt.Errorf("photos: unknown rendition")
	}
	if hash == "" {
		return "", nil, syncengine.ErrNotFound
	}
	// Serve only blobs this object holds a counted reference to.
	if hash != obj.BlobID && !slices.Contains(store.MetadataBlobIDs(obj.Metadata), strings.ToLower(hash)) {
		return "", nil, syncengine.ErrNotFound
	}
	body, err = s.Store.GetBlobPlaintext(hash)
	return mime, body, err
}

func PublicMeta(m Meta) map[string]any {
	out := map[string]any{
		"name": m.Name, "mime": m.MIME, "width": m.Width, "height": m.Height,
		"orientation": m.Orientation, "checksum": m.Checksum,
	}
	if m.Kind != "" {
		out["kind"] = m.Kind
	}
	if m.CameraMake != "" {
		out["camera_make"] = m.CameraMake
	}
	if m.CameraModel != "" {
		out["camera_model"] = m.CameraModel
	}
	if m.TakenAtMS != 0 {
		out["taken_at_ms"] = m.TakenAtMS
	}
	if m.DurationMS != 0 {
		out["duration_ms"] = m.DurationMS
	}
	if m.PreviewHash != "" {
		out["preview_hash"] = m.PreviewHash
	}
	if m.ThumbHash != "" {
		out["thumb_hash"] = m.ThumbHash
	}
	if m.LivePairHash != "" {
		out["live_pair_hash"] = m.LivePairHash
	}
	if m.Perceptual != "" {
		out["phash"] = m.Perceptual
	}
	if m.HasGPS {
		out["has_gps"] = true
	}
	if len(m.Albums) > 0 {
		out["albums"] = m.Albums
	}
	return out
}

func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = config.DefaultMaxBlobBytes
	}
	return io.ReadAll(io.LimitReader(r, max+1))
}

func HasLatLon(raw json.RawMessage) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	for _, k := range []string{"gps", "lat", "lon", "latitude", "longitude"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}
