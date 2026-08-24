package authz_test

import (
	"testing"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
)

func TestDenyByDefault(t *testing.T) {
	if err := authz.Allow(nil, authz.SyncRead, ""); err == nil {
		t.Fatal("nil device")
	}
}

func TestRevokedDenied(t *testing.T) {
	now := int64(1)
	d := &identity.Device{Role: identity.RoleAgent, Permissions: identity.DefaultPermissions(identity.RoleAgent), RevokedAt: &now}
	if err := authz.Allow(d, authz.SyncRead, ""); err == nil {
		t.Fatal("revoked")
	}
}

func TestDAVCannotSync(t *testing.T) {
	d := &identity.Device{Role: identity.RoleDAV, Permissions: identity.DefaultPermissions(identity.RoleDAV)}
	if err := authz.Allow(d, authz.SyncRead, ""); err == nil {
		t.Fatal("dav sync")
	}
	if err := authz.Allow(d, authz.DeviceRevoke, ""); err == nil {
		t.Fatal("dav admin")
	}
}

func TestCollectionACL(t *testing.T) {
	d := &identity.Device{Role: identity.RoleClient, Permissions: identity.Permissions{
		Role: identity.RoleClient, Collections: []string{"only-this"}, Protocols: []string{"sync"},
	}}
	if err := authz.Allow(d, authz.SyncRead, "only-this"); err != nil {
		t.Fatal(err)
	}
	if err := authz.Allow(d, authz.SyncRead, "other"); err == nil {
		t.Fatal("other collection")
	}
}

func TestDAVRequiresWebDAVProtocol(t *testing.T) {
	d := &identity.Device{Role: identity.RoleDAV, Permissions: identity.DefaultPermissions(identity.RoleDAV)}
	if err := authz.Allow(d, authz.DAVRead, ""); err != nil {
		t.Fatal(err)
	}
	d.Permissions.Protocols = []string{"caldav"}
	if err := authz.Allow(d, authz.DAVWrite, ""); err == nil {
		t.Fatal("caldav-only must not write files")
	}
}

func TestCalDAVRequiresProtocol(t *testing.T) {
	d := &identity.Device{Role: identity.RoleDAV, Permissions: identity.DefaultPermissions(identity.RoleDAV)}
	if err := authz.Allow(d, authz.CalDAVRead, ""); err != nil {
		t.Fatal(err)
	}
	d.Permissions.Protocols = []string{"webdav"}
	if err := authz.Allow(d, authz.CalDAVWrite, ""); err == nil {
		t.Fatal("webdav-only must not write calendars")
	}
}

func TestCardDAVRequiresProtocol(t *testing.T) {
	d := &identity.Device{Role: identity.RoleDAV, Permissions: identity.DefaultPermissions(identity.RoleDAV)}
	if err := authz.Allow(d, authz.CardDAVRead, ""); err != nil {
		t.Fatal(err)
	}
	d.Permissions.Protocols = []string{"webdav"}
	if err := authz.Allow(d, authz.CardDAVWrite, ""); err == nil {
		t.Fatal("webdav-only must not write contacts")
	}
}

func TestPhotosRequiresProtocol(t *testing.T) {
	d := &identity.Device{Role: identity.RoleDAV, Permissions: identity.DefaultPermissions(identity.RoleDAV)}
	if err := authz.Allow(d, authz.PhotosRead, ""); err == nil {
		t.Fatal("default dav must not read photos")
	}
	d.Permissions.Protocols = []string{"photos"}
	if err := authz.Allow(d, authz.PhotosRead, ""); err != nil {
		t.Fatal(err)
	}
	agent := &identity.Device{Role: identity.RoleAgent, Permissions: identity.DefaultPermissions(identity.RoleAgent)}
	if err := authz.Allow(agent, authz.PhotosRead, ""); err != nil {
		t.Fatal(err)
	}
}

func TestClientCannotAdmin(t *testing.T) {
	d := &identity.Device{Role: identity.RoleClient, Permissions: identity.DefaultPermissions(identity.RoleClient)}
	if err := authz.Allow(d, authz.DeviceRevoke, ""); err == nil {
		t.Fatal("client revoke")
	}
	if err := authz.Allow(d, authz.BackupRestore, ""); err == nil {
		t.Fatal("client restore")
	}
	if err := authz.Allow(d, authz.SyncWrite, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCannotBackup(t *testing.T) {
	d := &identity.Device{Role: identity.RoleAgent, Permissions: identity.DefaultPermissions(identity.RoleAgent)}
	if err := authz.Allow(d, authz.BackupCreate, ""); err == nil {
		t.Fatal("agent backup")
	}
	if err := authz.Allow(d, authz.PhotosWrite, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDAVPhotosWriteRequiresScope(t *testing.T) {
	d := &identity.Device{Role: identity.RoleDAV, Permissions: identity.DefaultPermissions(identity.RoleDAV)}
	if err := authz.Allow(d, authz.PhotosWrite, ""); err == nil {
		t.Fatal("default dav photos write")
	}
	d.Permissions.Protocols = []string{"photos"}
	if err := authz.Allow(d, authz.PhotosWrite, ""); err != nil {
		t.Fatal(err)
	}
	if err := authz.Allow(d, authz.DAVWrite, ""); err == nil {
		t.Fatal("photos-only must not write files")
	}
}

func TestUnknownActionDenied(t *testing.T) {
	d := &identity.Device{Role: identity.RoleAdmin, Permissions: identity.DefaultPermissions(identity.RoleAdmin)}
	if err := authz.Allow(d, authz.Action("nope"), ""); err == nil {
		t.Fatal("unknown action")
	}
}

func TestAdminLocal(t *testing.T) {
	d := &identity.Device{ID: "admin-local", Role: identity.RoleAdmin, Permissions: identity.DefaultPermissions(identity.RoleAdmin)}
	if err := authz.Allow(d, authz.DeviceRevoke, ""); err != nil {
		t.Fatal(err)
	}
}
