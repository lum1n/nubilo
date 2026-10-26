package syncengine

import (
	"context"
	"fmt"

	"nubilo/internal/identity"
	"nubilo/internal/ids"
)

// LocalOperatorID is a fixed ULID used as origin_device for local admin mutations
// (UI / operator tooling). It is not a real paired device row.
const LocalOperatorID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// LocalOperator returns a synthetic system admin device for Push from local
// tooling. It is not bound to a user and may write any collection.
func LocalOperator() *identity.Device {
	return &identity.Device{
		ID:          LocalOperatorID,
		Name:        "local-operator",
		Role:        identity.RoleAdmin,
		Permissions: identity.DefaultPermissions(identity.RoleAdmin),
	}
}

// LocalOperatorAs returns the local operator acting as user u, so writes are
// checked against and attributed to that user ("act as" in the admin UI).
func LocalOperatorAs(u *identity.User) *identity.Device {
	d := LocalOperator()
	if u != nil {
		d.UserID = u.ID
		d.UserRole = u.Role
		d.UserStatus = u.Status
	}
	return d
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

// DefaultCollections are created for every new user.
var DefaultCollections = []struct{ Kind, Name string }{
	{"files", "Files"},
	{"calendar", "Personal"},
	{"addressbook", "Contacts"},
	{"photos", "Photos"},
}

// EnsureUserDefaults creates the default collections a user starts with.
func (e *Engine) EnsureUserDefaults(ctx context.Context, userID string) error {
	for _, d := range DefaultCollections {
		if _, err := e.EnsureNamedCollection(ctx, userID, d.Kind, d.Name); err != nil {
			return err
		}
	}
	return nil
}
