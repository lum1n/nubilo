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
	DAVRead       Action = "dav.read"
	DAVWrite      Action = "dav.write"
	CalDAVRead    Action = "caldav.read"
	CalDAVWrite   Action = "caldav.write"
	CardDAVRead   Action = "carddav.read"
	CardDAVWrite  Action = "carddav.write"
	PhotosRead    Action = "photos.read"
	PhotosWrite   Action = "photos.write"
)

func Allow(d *identity.Device, action Action, collectionID string) error {
	if d == nil {
		return ErrDenied
	}
	if d.Revoked() {
		return ErrDenied
	}
	switch action {
	case SyncRead:
		if !d.Permissions.HasProtocol("sync") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case SyncWrite:
		if !d.Permissions.HasProtocol("sync") {
			return ErrDenied
		}
		if d.Role == identity.RoleDAV {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case DeviceList:
		return nil // any authenticated device may list (names/ids only)
	case DAVRead:
		if !d.Permissions.HasProtocol("webdav") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case DAVWrite:
		if !d.Permissions.HasProtocol("webdav") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case CalDAVRead:
		if !d.Permissions.HasProtocol("caldav") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case CalDAVWrite:
		if !d.Permissions.HasProtocol("caldav") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case CardDAVRead:
		if !d.Permissions.HasProtocol("carddav") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case CardDAVWrite:
		if !d.Permissions.HasProtocol("carddav") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case PhotosRead:
		if !d.Permissions.HasProtocol("photos") && !d.Permissions.HasProtocol("sync") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case PhotosWrite:
		if d.Role == identity.RoleDAV {
			if !d.Permissions.HasProtocol("photos") {
				return ErrDenied
			}
		} else if !d.Permissions.HasProtocol("photos") && !d.Permissions.HasProtocol("sync") {
			return ErrDenied
		}
		if collectionID != "" && !d.Permissions.CanCollection(collectionID) {
			return ErrDenied
		}
		return nil
	case DeviceRevoke, DeviceRename, MetricsRead, BackupCreate, BackupRestore, PairStart:
		if d.Permissions.Admin || d.Role == identity.RoleAdmin || d.ID == "admin-local" {
			return nil
		}
		return ErrDenied
	default:
		return ErrDenied
	}
}
