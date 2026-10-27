package syncengine

import (
	"context"
	"errors"

	"nubilo/internal/identity"
	"nubilo/internal/ids"
)

// ErrTreeConflict is returned when an object in a collection tree could not
// be deleted (another device changed it, or the caller may not).
var ErrTreeConflict = errors.New("syncengine: collection tree delete conflict")

// IsAncestor reports whether id is maybeChild or one of its ancestors.
func (e *Engine) IsAncestor(ctx context.Context, id, maybeChild string) bool {
	cur := maybeChild
	for i := 0; i < 32 && cur != ""; i++ {
		if cur == id {
			return true
		}
		c, err := e.GetCollection(ctx, cur)
		if err != nil {
			return false
		}
		cur = c.ParentID
	}
	return false
}

// DeleteCollectionTree deletes col, its child collections of the same kind
// and every object in them as dev, so other devices see the deletes in the
// journal, then tombstones the collections. Callers check access first.
func (e *Engine) DeleteCollectionTree(ctx context.Context, dev *identity.Device, col *Collection) error {
	children, err := e.ChildCollections(ctx, "", col.Kind, col.ID)
	if err != nil {
		return err
	}
	for i := range children {
		if err := e.DeleteCollectionTree(ctx, dev, &children[i]); err != nil {
			return err
		}
	}
	objs, err := e.ListObjects(ctx, col.ID)
	if err != nil {
		return err
	}
	for i := range objs {
		res, err := e.Push(ctx, dev, ids.New(), []ChangeInput{{
			ObjectID: objs[i].ID, CollectionID: col.ID, Op: OpDelete, BaseRevision: objs[i].Revision, Force: true,
		}})
		if err != nil {
			return err
		}
		if res[0].Status != "ok" {
			return ErrTreeConflict
		}
	}
	return e.TombstoneCollection(ctx, col.ID)
}
