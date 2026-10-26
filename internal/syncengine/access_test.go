package syncengine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"nubilo/internal/authz"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

type family struct {
	ctx        context.Context
	st         *store.Store
	eng        *syncengine.Engine
	ids        *identity.Service
	alice, bob *identity.Device
	aliceFiles *syncengine.Collection
}

func newFamily(t *testing.T) *family {
	t.Helper()
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	dir := t.TempDir()
	key := make([]byte, 32)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	f := &family{ctx: ctx, st: st, eng: syncengine.New(st), ids: identity.NewService(st)}
	f.ids.OnUserCreated = func(ctx context.Context, u *identity.User) error { return f.eng.EnsureUserDefaults(ctx, u.ID) }
	f.alice = f.member(t, "alice")
	f.bob = f.member(t, "bob")
	f.aliceFiles, err = f.eng.FindChildCollection(ctx, f.alice.UserID, "files", "", "Files")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *family) member(t *testing.T, name string) *identity.Device {
	t.Helper()
	u, err := f.ids.CreateUser(f.ctx, name, "", identity.UserMember)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ncrypto.GenerateEd25519()
	d, err := f.ids.Enroll(f.ctx, u.ID, name+"-phone", pub, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *family) blob(t *testing.T, dev *identity.Device, payload string) string {
	t.Helper()
	h, _, err := f.st.PutUserBlob(f.ctx, dev.UserID, bytes.NewReader([]byte(payload)), "")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (f *family) create(t *testing.T, dev *identity.Device, colID, payload string) syncengine.PushResult {
	t.Helper()
	h := f.blob(t, dev, payload)
	res, err := f.eng.Push(f.ctx, dev, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: colID, Op: syncengine.OpCreate,
		ContentHash: h, BlobID: h, Size: int64(len(payload)), Metadata: json.RawMessage(`{"name":"` + payload + `"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return res[0]
}

func (f *family) access(t *testing.T, dev *identity.Device, colID string) authz.Access {
	t.Helper()
	a, err := f.eng.AccessFor(f.ctx, dev, colID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAccessLevels(t *testing.T) {
	f := newFamily(t)
	if got := f.access(t, f.alice, f.aliceFiles.ID); got != authz.AccessOwner {
		t.Fatalf("owner access %v", got)
	}
	if got := f.access(t, f.bob, f.aliceFiles.ID); got != authz.AccessNone {
		t.Fatalf("stranger access %v", got)
	}
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "ro", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if got := f.access(t, f.bob, f.aliceFiles.ID); got != authz.AccessRead {
		t.Fatalf("ro share access %v", got)
	}
	if r := f.create(t, f.bob, f.aliceFiles.ID, "from bob"); r.Status != "error" || r.Error != "unauthorized" {
		t.Fatalf("ro share push %+v", r)
	}
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if got := f.access(t, f.bob, f.aliceFiles.ID); got != authz.AccessWrite {
		t.Fatalf("rw share access %v", got)
	}
	if r := f.create(t, f.bob, f.aliceFiles.ID, "from bob"); r.Status != "ok" {
		t.Fatalf("rw share push %+v", r)
	}
	if err := f.eng.RemoveShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID); err != nil {
		t.Fatal(err)
	}
	if got := f.access(t, f.bob, f.aliceFiles.ID); got != authz.AccessNone {
		t.Fatalf("after unshare %v", got)
	}
}

func TestShareInheritedByChildren(t *testing.T) {
	f := newFamily(t)
	child, err := f.eng.CreateCollection(f.ctx, f.bob.UserID, "files", "Sub", f.aliceFiles.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.OwnerUserID != f.alice.UserID || child.RootID != f.aliceFiles.ID {
		t.Fatalf("child owner %s root %s", child.OwnerUserID, child.RootID)
	}
	if got := f.access(t, f.bob, child.ID); got != authz.AccessNone {
		t.Fatalf("child before share %v", got)
	}
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if got := f.access(t, f.bob, child.ID); got != authz.AccessWrite {
		t.Fatalf("child after share %v", got)
	}
	if err := f.eng.SetShare(f.ctx, child.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err == nil {
		t.Fatal("shares on non-root collections must be rejected")
	}
}

func TestCrossOwnerMoveRejected(t *testing.T) {
	f := newFamily(t)
	bobFiles, err := f.eng.FindChildCollection(f.ctx, f.bob.UserID, "files", "", "Files")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	r := f.create(t, f.alice, f.aliceFiles.ID, "alice doc")
	obj, err := f.eng.GetObject(f.ctx, r.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	// Bob can write both collections but must not move Alice's file into his own.
	res, err := f.eng.Push(f.ctx, f.bob, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: bobFiles.ID, Op: syncengine.OpUpdate, BaseRevision: obj.Revision,
		ContentHash: obj.ContentHash, BlobID: obj.BlobID, Size: obj.Size, Metadata: obj.Metadata,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != "error" || res[0].Error != "unauthorized" {
		t.Fatalf("cross-owner move %+v", res[0])
	}
	// Nor can a stranger pull an object into their collection by id.
	f.eng.RemoveShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID)
	res, _ = f.eng.Push(f.ctx, f.bob, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: bobFiles.ID, Op: syncengine.OpDelete, BaseRevision: obj.Revision, Force: true,
	}})
	if res[0].Status == "ok" {
		t.Fatal("stranger deleted another user's object")
	}
	if err := f.eng.SetCollectionParent(f.ctx, bobFiles.ID, f.aliceFiles.ID); err == nil {
		t.Fatal("collection moved across owners")
	}
}

func TestForeignBlobCannotBeAttached(t *testing.T) {
	f := newFamily(t)
	secret := f.blob(t, f.alice, "alice's secret")
	bobFiles, _ := f.eng.FindChildCollection(f.ctx, f.bob.UserID, "files", "", "Files")
	res, err := f.eng.Push(f.ctx, f.bob, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: bobFiles.ID, Op: syncengine.OpCreate,
		ContentHash: secret, BlobID: secret, Size: 14, Metadata: json.RawMessage(`{"name":"stolen"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status == "ok" {
		t.Fatal("bob attached alice's blob by guessing its hash")
	}
	if ok, _ := f.eng.BlobReadable(f.ctx, f.bob.UserID, secret); ok {
		t.Fatal("bob can read alice's blob")
	}
	if ok, _ := f.eng.BlobReadable(f.ctx, f.alice.UserID, secret); !ok {
		t.Fatal("alice cannot read her blob")
	}
}

func TestChangesFilteredBeforeLimit(t *testing.T) {
	f := newFamily(t)
	bobFiles, _ := f.eng.FindChildCollection(f.ctx, f.bob.UserID, "files", "", "Files")
	f.create(t, f.bob, bobFiles.ID, "bob-1")
	var batch []syncengine.ChangeInput
	for i := 0; i < 1000; i++ {
		h := f.blob(t, f.alice, "a"+ids.New())
		batch = append(batch, syncengine.ChangeInput{
			ObjectID: ids.New(), CollectionID: f.aliceFiles.ID, Op: syncengine.OpCreate,
			ContentHash: h, BlobID: h, Size: 27, Metadata: json.RawMessage(`{}`),
		})
		if len(batch) == 500 {
			if _, err := f.eng.Push(f.ctx, f.alice, ids.New(), batch); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
	}
	f.create(t, f.bob, bobFiles.ID, "bob-2")
	res, err := f.eng.GetChanges(f.ctx, syncengine.ScopeOf(f.bob), 0, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 2 || res.HasMore {
		t.Fatalf("bob sees %d changes (has_more=%v), want his 2", len(res.Changes), res.HasMore)
	}
	for _, c := range res.Changes {
		if c.CollectionID != bobFiles.ID {
			t.Fatalf("leaked change from %s", c.CollectionID)
		}
	}
	res, _ = f.eng.GetChanges(f.ctx, syncengine.ScopeOf(f.alice), 0, 200, "")
	if len(res.Changes) != 200 || !res.HasMore {
		t.Fatalf("alice page %d has_more=%v", len(res.Changes), res.HasMore)
	}
}

func TestDisabledUserLosesAccess(t *testing.T) {
	f := newFamily(t)
	if err := f.ids.SetUserStatus(f.ctx, f.alice.UserID, identity.UserDisabled); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.eng.Access(f.ctx, f.alice.UserID, f.aliceFiles.ID); got != authz.AccessNone {
		t.Fatalf("disabled owner access %v", got)
	}
	dev, err := f.ids.Get(f.ctx, f.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Revoked() {
		t.Fatal("disabling a user must revoke their devices")
	}
}

func TestCollectionsRevTracksShares(t *testing.T) {
	f := newFamily(t)
	before, err := f.eng.CollectionsRev(f.ctx, syncengine.ScopeOf(f.bob))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "ro", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	after, _ := f.eng.CollectionsRev(f.ctx, syncengine.ScopeOf(f.bob))
	if after == before {
		t.Fatal("collections_rev unchanged after share")
	}
	cols, _ := f.eng.GetCollections(f.ctx, syncengine.ScopeOf(f.bob))
	found := false
	for _, c := range cols {
		if c.ID == f.aliceFiles.ID {
			found = c.Access == "ro" && c.OwnerName == "alice"
		}
	}
	if !found {
		t.Fatal("shared collection missing from bob's list")
	}
	f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID)
	again, _ := f.eng.CollectionsRev(f.ctx, syncengine.ScopeOf(f.bob))
	if again == after {
		t.Fatal("collections_rev unchanged after access change")
	}
}

func TestIdempotencyKeysPerDevice(t *testing.T) {
	f := newFamily(t)
	bobFiles, _ := f.eng.FindChildCollection(f.ctx, f.bob.UserID, "files", "", "Files")
	ha := f.blob(t, f.alice, "x")
	hb := f.blob(t, f.bob, "y")
	ra, err := f.eng.Push(f.ctx, f.alice, "same-key", []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: f.aliceFiles.ID, Op: syncengine.OpCreate, ContentHash: ha, BlobID: ha, Size: 1,
	}})
	if err != nil || ra[0].Status != "ok" {
		t.Fatalf("alice %v %+v", err, ra)
	}
	rb, err := f.eng.Push(f.ctx, f.bob, "same-key", []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: bobFiles.ID, Op: syncengine.OpCreate, ContentHash: hb, BlobID: hb, Size: 1,
	}})
	if err != nil || rb[0].Status != "ok" || rb[0].ObjectID == ra[0].ObjectID {
		t.Fatalf("bob reused alice's idempotency result: %v %+v", err, rb)
	}
}

func TestMovedOutOfShareIsRedacted(t *testing.T) {
	f := newFamily(t)
	shared, err := f.eng.CreateCollection(f.ctx, f.alice.UserID, "files", "Shared", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.SetShare(f.ctx, shared.ID, "user", f.bob.UserID, "ro", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	r := f.create(t, f.alice, shared.ID, "public note")
	obj, _ := f.eng.GetObject(f.ctx, r.ObjectID)
	secret := f.blob(t, f.alice, "salary data")
	res, err := f.eng.Push(f.ctx, f.alice, ids.New(), []syncengine.ChangeInput{{
		ObjectID: obj.ID, CollectionID: f.aliceFiles.ID, Op: syncengine.OpUpdate, BaseRevision: obj.Revision,
		ContentHash: secret, BlobID: secret, Size: 11, Metadata: json.RawMessage(`{"name":"SECRET-salary.xlsx"}`),
	}})
	if err != nil || res[0].Status != "ok" {
		t.Fatalf("move %v %+v", err, res)
	}
	ch, err := f.eng.GetChanges(f.ctx, syncengine.ScopeOf(f.bob), 0, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Changes) != 1 {
		t.Fatalf("bob changes %+v", ch.Changes)
	}
	c := ch.Changes[0]
	if !c.Deleted || c.BlobID != "" || len(c.Metadata) != 0 {
		t.Fatalf("leaked moved object: %+v", c)
	}
}

func TestMetadataKeyCaseCannotSmuggleBlob(t *testing.T) {
	f := newFamily(t)
	secret := f.blob(t, f.alice, "alice's diary")
	bobFiles, _ := f.eng.FindChildCollection(f.ctx, f.bob.UserID, "files", "", "Files")
	h := f.blob(t, f.bob, "bob photo")
	for _, meta := range []string{
		`{"name":"p.jpg","Preview_Hash":"` + secret + `"}`,
		`{"name":"p.jpg","note":"` + secret + `"}`,
	} {
		res, err := f.eng.Push(f.ctx, f.bob, ids.New(), []syncengine.ChangeInput{{
			ObjectID: ids.New(), CollectionID: bobFiles.ID, Op: syncengine.OpCreate, Kind: "photo",
			ContentHash: h, BlobID: h, Size: 9, Metadata: json.RawMessage(meta),
		}})
		if err != nil {
			t.Fatal(err)
		}
		_ = res
		if ok, _ := f.eng.BlobReadable(f.ctx, f.bob.UserID, secret); ok {
			t.Fatalf("bob can read alice's blob via metadata %s (push %+v)", meta, res[0])
		}
	}
}

func TestRWSharerCannotAttachOwnersBlob(t *testing.T) {
	f := newFamily(t)
	private := f.blob(t, f.alice, "private")
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	res, _ := f.eng.Push(f.ctx, f.bob, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: f.aliceFiles.ID, Op: syncengine.OpCreate,
		ContentHash: private, BlobID: private, Size: 7,
	}})
	if res[0].Status == "ok" {
		t.Fatal("rw sharer attached owner's private blob by hash")
	}
}

func TestDisabledOwnerSharesGoDark(t *testing.T) {
	f := newFamily(t)
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if err := f.ids.SetUserStatus(f.ctx, f.alice.UserID, identity.UserDisabled); err != nil {
		t.Fatal(err)
	}
	if got := f.access(t, f.bob, f.aliceFiles.ID); got != authz.AccessNone {
		t.Fatalf("share of disabled owner: %v", got)
	}
	cols, _ := f.eng.GetCollections(f.ctx, syncengine.ScopeOf(f.bob))
	for _, c := range cols {
		if c.ID == f.aliceFiles.ID {
			t.Fatal("disabled owner's collection still listed")
		}
	}
}
