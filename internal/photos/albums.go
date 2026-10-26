package photos

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

// Album is a root collection of kind "album". Its items are album_item
// objects that reference a photo's blobs (counted like any other object, so
// the photo stays readable in the album after it leaves the library).
type Album struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Access    string `json:"access"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func albumFrom(c *syncengine.Collection) Album {
	return Album{ID: c.ID, Name: c.Name, Owner: c.OwnerName, Access: c.Access, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func (s Service) CreateAlbum(ctx context.Context, userID, name string) (*Album, error) {
	name = strings.TrimSpace(name)
	if userID == "" {
		return nil, ErrNoUser
	}
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "/\\\x00") {
		return nil, fmt.Errorf("photos: invalid album name")
	}
	c, err := s.Engine.CreateCollection(ctx, userID, AlbumKind, name, "", nil)
	if err != nil {
		return nil, err
	}
	c.Access = authz.AccessOwner.String()
	a := albumFrom(c)
	return &a, nil
}

// Albums lists userID's own albums followed by albums shared with them.
func (s Service) Albums(ctx context.Context, userID string) ([]Album, error) {
	own, err := s.Engine.ChildCollections(ctx, userID, AlbumKind, "")
	if err != nil {
		return nil, err
	}
	shared, err := s.Engine.SharedRoots(ctx, userID, AlbumKind)
	if err != nil {
		return nil, err
	}
	out := make([]Album, 0, len(own)+len(shared))
	for i := range own {
		own[i].Access = authz.AccessOwner.String()
		out = append(out, albumFrom(&own[i]))
	}
	for i := range shared {
		out = append(out, albumFrom(&shared[i]))
	}
	return out, nil
}

func (s Service) album(ctx context.Context, dev *identity.Device, albumID string, want authz.Access) (*syncengine.Collection, error) {
	c, err := s.Engine.GetCollection(ctx, albumID)
	if err != nil {
		return nil, err
	}
	if c.Kind != AlbumKind || c.DeletedAt != nil {
		return nil, syncengine.ErrNotFound
	}
	access, err := s.Engine.AccessFor(ctx, dev, albumID)
	if err != nil {
		return nil, err
	}
	if access == authz.AccessNone {
		return nil, syncengine.ErrNotFound
	}
	if access < want {
		return nil, ErrForbidden
	}
	return c, nil
}

func (s Service) AlbumItems(ctx context.Context, dev *identity.Device, albumID, after string, limit int) ([]syncengine.Object, string, error) {
	if _, err := s.album(ctx, dev, albumID, authz.AccessRead); err != nil {
		return nil, "", err
	}
	return s.ListCollection(ctx, albumID, AlbumItemKind, after, limit)
}

// AddToAlbum adds photos the device can read to an album it can write.
// Photos already in the album are skipped.
func (s Service) AddToAlbum(ctx context.Context, dev *identity.Device, albumID string, photoIDs []string) ([]syncengine.Object, error) {
	if dev == nil || (dev.UserID == "" && !dev.System()) {
		return nil, ErrNoUser
	}
	if _, err := s.album(ctx, dev, albumID, authz.AccessWrite); err != nil {
		return nil, err
	}
	var changes []syncengine.ChangeInput
	for _, pid := range photoIDs {
		photo, err := s.Get(ctx, dev, pid)
		if err != nil {
			return nil, err
		}
		if photo.Kind == AlbumItemKind {
			pid = AlbumItemPhotoID(photo.Metadata)
		}
		if existing, err := s.findAlbumItem(ctx, albumID, pid); err == nil && existing != nil {
			continue
		}
		// Reading the photo grants the right to reference its blobs.
		for _, h := range append([]string{photo.BlobID}, store.MetadataBlobIDs(photo.Metadata)...) {
			if err := s.Store.AddBlobRef(ctx, dev.UserID, h); err != nil {
				return nil, err
			}
		}
		meta, err := syncengine.MergeJSON(photo.Metadata, mustJSON(map[string]string{"photo_id": pid}))
		if err != nil {
			return nil, err
		}
		changes = append(changes, syncengine.ChangeInput{
			ObjectID: ids.New(), CollectionID: albumID, Kind: AlbumItemKind, Op: syncengine.OpCreate,
			ContentHash: photo.ContentHash, BlobID: photo.BlobID, Size: photo.Size, Metadata: meta, Force: true,
		})
	}
	if len(changes) == 0 {
		return nil, nil
	}
	res, err := s.Engine.Push(ctx, dev, ids.New(), changes)
	if err != nil {
		return nil, err
	}
	out := make([]syncengine.Object, 0, len(res))
	for _, r := range res {
		if r.Status != "ok" {
			return nil, fmt.Errorf("photos: album add rejected: %s", r.Error)
		}
		o, err := s.Engine.GetObject(ctx, r.ObjectID)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, nil
}

func (s Service) findAlbumItem(ctx context.Context, albumID, photoID string) (*syncengine.Object, error) {
	row := s.Store.DB.QueryRowContext(ctx, `
		SELECT id FROM objects WHERE collection_id = ? AND kind = ? AND deleted_at IS NULL
		AND json_extract(metadata, '$.photo_id') = ? LIMIT 1`, albumID, AlbumItemKind, photoID)
	var id string
	if err := row.Scan(&id); err != nil {
		return nil, err
	}
	return s.Engine.GetObject(ctx, id)
}

// AlbumItemPhotoID returns the library photo an album item was added from.
func AlbumItemPhotoID(meta json.RawMessage) string {
	var m struct {
		PhotoID string `json:"photo_id"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return ""
	}
	return m.PhotoID
}
