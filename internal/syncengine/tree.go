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

// IsAncestor reports whether id is maybeChild or one of its ancestors. It
// walks to the top however deep the tree is (a depth cap would let a move
// create a cycle), and answers true when it cannot tell, so callers refuse.
func (e *Engine) IsAncestor(ctx context.Context, id, maybeChild string) bool {
	seen := map[string]bool{}
	for cur := maybeChild; cur != ""; {
		if cur == id || seen[cur] {
			return true
		}
		seen[cur] = true
		c, err := e.GetCollection(ctx, cur)
		if err != nil {
			return true
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
