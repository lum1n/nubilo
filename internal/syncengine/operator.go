package syncengine

import (
	"context"
	"fmt"

	"nubilo/internal/ids"
	"nubilo/internal/identity"
)

// LocalOperatorID is a fixed ULID used as origin_device for local admin mutations
// (UI / operator tooling). It is not a real paired device row.
const LocalOperatorID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// LocalOperator returns a synthetic admin device for Push from local tooling.
func LocalOperator() *identity.Device {
	return &identity.Device{
		ID:          LocalOperatorID,
		Name:        "local-operator",
		Role:        identity.RoleAdmin,
		Permissions: identity.DefaultPermissions(identity.RoleAdmin),
	}
}

// DeleteObject tombstones a live object via the sync journal (same path as CalDAV/WebDAV).
func (e *Engine) DeleteObject(ctx context.Context, objectID string) error {
	obj, err := e.GetObject(ctx, objectID)
	if err != nil {
		return err
	}
	if obj.DeletedAt != nil {
		return nil
	}
	res, err := e.Push(ctx, LocalOperator(), ids.New(), []ChangeInput{{
		ObjectID: obj.ID, CollectionID: obj.CollectionID, Op: OpDelete,
		BaseRevision: obj.Revision, Force: true,
	}})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		msg := "delete failed"
		if len(res) > 0 && res[0].Error != "" {
			msg = res[0].Error
		} else if len(res) > 0 {
			msg = res[0].Status
		}
		return fmt.Errorf("sync: %s", msg)
	}
	return nil
}

// DeleteCollection deletes all live objects then tombstones the collection.
func (e *Engine) DeleteCollection(ctx context.Context, id string) error {
	if _, err := e.GetCollection(ctx, id); err != nil {
		return err
	}
	objs, err := e.ListObjects(ctx, id)
	if err != nil {
		return err
	}
	for i := range objs {
		if err := e.DeleteObject(ctx, objs[i].ID); err != nil {
			return err
		}
	}
	return e.TombstoneCollection(ctx, id)
}
