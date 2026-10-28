package syncengine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"nubilo/internal/ids"
	"nubilo/internal/syncengine"
)

func TestQuota(t *testing.T) {
	f := newFamily(t)
	if err := f.ids.SetUserQuota(f.ctx, f.alice.UserID, 10); err != nil {
		t.Fatal(err)
	}
	if r := f.create(t, f.alice, f.aliceFiles.ID, "123456"); r.Status != "ok" {
		t.Fatalf("first write within quota: %+v", r)
	}
	if r := f.create(t, f.alice, f.aliceFiles.ID, "abcdef"); r.Error != syncengine.ErrQuotaExceeded {
		t.Fatalf("write past quota: %+v", r)
	}

	// Under-reporting the size of a blob-backed object does not dodge it.
	h := f.blob(t, f.alice, "zzzzzz")
	res, err := f.eng.Push(f.ctx, f.alice, ids.New(), []syncengine.ChangeInput{{
		ObjectID: ids.New(), CollectionID: f.aliceFiles.ID, Op: syncengine.OpCreate,
		ContentHash: h, BlobID: h, Size: 1, Metadata: json.RawMessage(`{"name":"z"}`),
	}})
	if err != nil || res[0].Error != syncengine.ErrQuotaExceeded {
		t.Fatalf("under-reported size: %+v %v", res, err)
	}

	// A member writing into Alice's shared folder uses Alice's quota.
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if r := f.create(t, f.bob, f.aliceFiles.ID, strings.Repeat("b", 5)); r.Error != syncengine.ErrQuotaExceeded {
		t.Fatalf("shared write past owner quota: %+v", r)
	}

	// Removing the quota lifts the limit.
	if err := f.ids.SetUserQuota(f.ctx, f.alice.UserID, 0); err != nil {
		t.Fatal(err)
	}
	if r := f.create(t, f.alice, f.aliceFiles.ID, "abcdef"); r.Status != "ok" {
		t.Fatalf("no quota: %+v", r)
	}
	u, err := f.ids.GetUser(f.ctx, f.alice.UserID)
	if err != nil || u.QuotaBytes != 0 {
		t.Fatalf("user %+v %v", u, err)
	}
}
