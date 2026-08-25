package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

var ErrNotPhoto = errors.New("photos: not a photo object")

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

func (s Service) Collection(ctx context.Context) (*syncengine.Collection, error) {
	return s.Engine.EnsureNamedCollection(ctx, CollectionKind, DefaultName)
}

func (s Service) Ingest(ctx context.Context, dev *identity.Device, original []byte, name string, albums []string) (*syncengine.Object, error) {
	if len(original) == 0 {
		return nil, fmt.Errorf("photos: empty body")
	}
	prep, err := Prepare(original, name, s.options())
	if err != nil {
		return nil, err
	}
	origHash := ncrypto.SHA256Hex(prep.Original)
	if _, _, err := s.Store.PutBlob(ctx, bytes.NewReader(prep.Original), origHash); err != nil {
		return nil, err
	}
	prep.Meta.Checksum = origHash
	if len(prep.Preview) > 0 {
		ph := ncrypto.SHA256Hex(prep.Preview)
		if _, _, err := s.Store.PutBlob(ctx, bytes.NewReader(prep.Preview), ph); err != nil {
			return nil, err
		}
		prep.Meta.PreviewHash = ph
	}
	if len(prep.Thumb) > 0 {
		th := ncrypto.SHA256Hex(prep.Thumb)
		if _, _, err := s.Store.PutBlob(ctx, bytes.NewReader(prep.Thumb), th); err != nil {
			return nil, err
		}
		prep.Meta.ThumbHash = th
	}
	prep.Meta.Albums = albums
	col, err := s.Collection(ctx)
	if err != nil {
		return nil, err
	}
	if existing, err := s.Engine.FindObjectByName(ctx, col.ID, prep.Meta.Name); err == nil && existing.ContentHash == origHash {
		return existing, nil
	}
	res, err := s.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID:     ids.New(),
		CollectionID: col.ID,
		Kind:         ObjectKind,
		Op:           syncengine.OpCreate,
		ContentHash:  origHash,
		BlobID:       origHash,
		Size:         int64(len(prep.Original)),
		Metadata:     EncodeMeta(prep.Meta),
		Force:        true,
	}})
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		return nil, fmt.Errorf("photos: push rejected")
	}
	return s.Engine.GetObject(ctx, res[0].ObjectID)
}

func (s Service) List(ctx context.Context) ([]syncengine.Object, error) {
	col, err := s.Collection(ctx)
	if err != nil {
		return nil, err
	}
	objs, err := s.Engine.ListObjects(ctx, col.ID)
	if err != nil {
		return nil, err
	}
	out := objs[:0]
	for _, o := range objs {
		if o.Kind == ObjectKind {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s Service) Get(ctx context.Context, id string) (*syncengine.Object, error) {
	obj, err := s.Engine.GetObject(ctx, id)
	if err != nil {
		return nil, err
	}
	if obj.Kind != ObjectKind {
		return nil, ErrNotPhoto
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
		max = 64 << 20
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
