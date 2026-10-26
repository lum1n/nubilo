package authz

import (
	"errors"

	"nubilo/internal/identity"
)

var ErrDenied = errors.New("authz: denied")

type Action string

const (
	SyncRead      Action = "sync.read"
	SyncWrite     Action = "sync.write"
	DeviceList    Action = "device.list"
	DeviceRevoke  Action = "device.revoke"
	DeviceRename  Action = "device.rename"
	MetricsRead   Action = "metrics.read"
	BackupCreate  Action = "backup.create"
	BackupRestore Action = "backup.restore"
	PairStart     Action = "pair.start"
	UserAdmin     Action = "user.admin"
	DAVRead       Action = "dav.read"
	DAVWrite      Action = "dav.write"
	CalDAVRead    Action = "caldav.read"
	CalDAVWrite   Action = "caldav.write"
	CardDAVRead   Action = "carddav.read"
	CardDAVWrite  Action = "carddav.write"
	PhotosRead    Action = "photos.read"
	PhotosWrite   Action = "photos.write"
	// CollectionAdmin covers deleting, renaming and sharing a collection.
	CollectionAdmin Action = "collection.admin"
)

// Access is a user's level of access to one collection.
type Access int

const (
	AccessNone Access = iota
	AccessRead
	AccessWrite
	AccessOwner
)

func (a Access) String() string {
	switch a {
	case AccessRead:
		return "ro"
	case AccessWrite:
		return "rw"
	case AccessOwner:
		return "owner"
	default:
		return "none"
	}
}

// Allow checks protocol and role permissions of a device. Collection-level
// access is checked separately with AllowCollection.
func Allow(d *identity.Device, action Action) error {
	if d == nil {
		return ErrDenied
	}
	if d.Revoked() {
		return ErrDenied
	}
	p := d.Permissions
	switch action {
	case SyncRead:
		return need(p.HasProtocol("sync"))
	case SyncWrite:
		return need(p.HasProtocol("sync") && d.Role != identity.RoleDAV)
	case DeviceList, CollectionAdmin:
		return nil // devices: own user's only; collections: Owner checked by AllowCollection
	case DAVRead, DAVWrite:
		return need(p.HasProtocol("webdav"))
	case CalDAVRead, CalDAVWrite:
		return need(p.HasProtocol("caldav"))
	case CardDAVRead, CardDAVWrite:
		return need(p.HasProtocol("carddav"))
	case PhotosRead:
		return need(p.HasProtocol("photos") || p.HasProtocol("sync"))
	case PhotosWrite:
		if d.Role == identity.RoleDAV {
			return need(p.HasProtocol("photos"))
		}
		return need(p.HasProtocol("photos") || p.HasProtocol("sync"))
	case DeviceRevoke, DeviceRename, MetricsRead, BackupCreate, BackupRestore, PairStart, UserAdmin:
		return need(d.IsAdmin())
	default:
		return ErrDenied
	}
}

// AllowCollection checks action (after Allow) against the caller's access to
// one collection: reads need Read, writes need Write, collection admin needs Owner.
func AllowCollection(d *identity.Device, action Action, access Access) error {
	if err := Allow(d, action); err != nil {
		return err
	}
	var want Access
	switch action {
	case CollectionAdmin:
		want = AccessOwner
	case SyncWrite, DAVWrite, CalDAVWrite, CardDAVWrite, PhotosWrite:
		want = AccessWrite
	default:
		want = AccessRead
	}
	return need(access >= want)
}

func need(ok bool) error {
	if ok {
		return nil
	}
	return ErrDenied
}
