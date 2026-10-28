package syncengine_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"nubilo/internal/ids"
	"nubilo/internal/syncengine"
)

// Each f.create costs len(payload) + len(`{"name":"<payload>"}`) = 2*len+11.
func TestQuota(t *testing.T) {
	f := newFamily(t)
	if err := f.ids.SetUserQuota(f.ctx, f.alice.UserID, 40); err != nil {
		t.Fatal(err)
	}
	if r := f.create(t, f.alice, f.aliceFiles.ID, "123456"); r.Status != "ok" { // 23
		t.Fatalf("first write within quota: %+v", r)
	}
	if r := f.create(t, f.alice, f.aliceFiles.ID, "abcdef"); r.Error != syncengine.ErrQuotaExceeded { // 46
		t.Fatalf("write past quota: %+v", r)
	}

	// A member writing into Alice's shared folder uses Alice's quota.
	if err := f.eng.SetShare(f.ctx, f.aliceFiles.ID, "user", f.bob.UserID, "rw", f.alice.UserID); err != nil {
		t.Fatal(err)
	}
	if r := f.create(t, f.bob, f.aliceFiles.ID, strings.Repeat("b", 9)); r.Error != syncengine.ErrQuotaExceeded { // 29
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

func quotaPush(t *testing.T, f *family, in syncengine.ChangeInput) syncengine.PushResult {
	t.Helper()
	in.ObjectID, in.Op = ids.New(), syncengine.OpCreate
	res, err := f.eng.Push(f.ctx, f.alice, ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		t.Fatal(err)
	}
	return res[0]
}

// Ways to store data without paying for it (security review 2026-10-28).
func TestQuotaCannotBeDodged(t *testing.T) {
	f := newFamily(t)
	if err := f.ids.SetUserQuota(f.ctx, f.alice.UserID, 100); err != nil {
		t.Fatal(err)
	}
	col := f.aliceFiles.ID

	// A huge reported size neither overflows the check nor sticks.
	if r := quotaPush(t, f, syncengine.ChangeInput{CollectionID: col, Size: math.MaxInt64, Metadata: json.RawMessage(`{}`)}); r.Error != "invalid size" {
		t.Fatalf("max size: %+v", r)
	}
	if r := quotaPush(t, f, syncengine.ChangeInput{CollectionID: col, Size: -5, Metadata: json.RawMessage(`{}`)}); r.Error != "invalid size" {
		t.Fatalf("negative size: %+v", r)
	}
	// A blob-backed object costs its real blob size, whatever it claims.
	h := f.blob(t, f.alice, strings.Repeat("x", 200))
	if r := quotaPush(t, f, syncengine.ChangeInput{CollectionID: col, BlobID: h, ContentHash: h, Size: 1, Metadata: json.RawMessage(`{}`)}); r.Error != syncengine.ErrQuotaExceeded {
		t.Fatalf("under-reported blob: %+v", r)
	}
	// Blobs named in metadata count too.
	if r := quotaPush(t, f, syncengine.ChangeInput{CollectionID: col, Metadata: json.RawMessage(`{"preview_hash":"` + h + `"}`)}); r.Error != syncengine.ErrQuotaExceeded {
		t.Fatalf("extra blob: %+v", r)
	}
	// So does metadata, which is capped.
	if r := quotaPush(t, f, syncengine.ChangeInput{CollectionID: col, Metadata: json.RawMessage(`{"name":"` + strings.Repeat("n", 200) + `"}`)}); r.Error != syncengine.ErrQuotaExceeded {
		t.Fatalf("big metadata: %+v", r)
	}
	if err := f.ids.SetUserQuota(f.ctx, f.alice.UserID, 0); err != nil {
		t.Fatal(err)
	}
	if r := quotaPush(t, f, syncengine.ChangeInput{CollectionID: col, Metadata: json.RawMessage(`{"name":"` + strings.Repeat("n", syncengine.MaxMetadataBytes) + `"}`)}); r.Error != "metadata too large" {
		t.Fatalf("metadata cap: %+v", r)
	}
	// Usage still adds up for everyone.
	if _, err := f.eng.UserUsage(f.ctx); err != nil {
		t.Fatal(err)
	}
}
