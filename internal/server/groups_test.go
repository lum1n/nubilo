package server_test

import (
	"context"
	"net/http"
	"testing"
)

// accessTo returns the access bob-like callers have on a collection as listed
// by /api/v1/collections ("" when not visible), and how often it is listed.
func accessTo(s *signer, colID string) (string, int) {
	list := s.json(http.MethodGet, "/api/v1/collections", nil, http.StatusOK)
	access, n := "", 0
	for _, c := range list["collections"].([]any) {
		m := c.(map[string]any)
		if m["id"] == colID {
			access, _ = m["access"].(string)
			n++
		}
	}
	return access, n
}

func TestGroupShares(t *testing.T) {
	f := startFamily(t)
	aliceFiles := f.files(t, f.alice)

	// Only admins manage groups.
	if code, _ := f.alice.do(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "Team"}); code != http.StatusForbidden {
		t.Fatalf("member created group: %d", code)
	}
	g := f.admin.json(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "Team", "members": []string{f.bob.dev.UserID}}, http.StatusCreated)
	gid := g["id"].(string)
	if code, _ := f.admin.do(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "team"}); code != http.StatusConflict {
		t.Fatalf("duplicate name (case-insensitive): %d", code)
	}
	if code, _ := f.admin.do(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "X", "members": []string{"nope"}}); code != http.StatusNotFound {
		t.Fatalf("unknown member: %d", code)
	}
	groups := f.alice.json(http.MethodGet, "/api/v1/groups", nil, http.StatusOK)["groups"].([]any)
	if len(groups) != 1 || len(groups[0].(map[string]any)["members"].([]any)) != 1 {
		t.Fatalf("groups %v", groups)
	}

	// Alice shares her Files read-only with the group: Bob, a member, sees it.
	if a, _ := accessTo(f.bob, aliceFiles); a != "" {
		t.Fatalf("visible before share: %s", a)
	}
	shares := f.alice.json(http.MethodPut, "/api/v1/collections/"+aliceFiles+"/shares", map[string]any{"principal_type": "group", "group_name": "Team", "access": "ro"}, http.StatusOK)
	sh := shares["shares"].([]any)[0].(map[string]any)
	if sh["principal_type"] != "group" || sh["group_name"] != "Team" {
		t.Fatalf("share %v", sh)
	}
	if a, _ := accessTo(f.bob, aliceFiles); a != "ro" {
		t.Fatalf("group ro: %q", a)
	}
	_, h := putBlob(f.bob, []byte("from bob"))
	if s := pushStatus(pushCreate(f.bob, aliceFiles, h, 8)); s != "error:unauthorized" {
		t.Fatalf("ro group write: %s", s)
	}

	// A direct rw share and the group ro share: the best wins, listed once.
	f.alice.json(http.MethodPut, "/api/v1/collections/"+aliceFiles+"/shares", map[string]any{"username": "bob", "access": "rw"}, http.StatusOK)
	if a, n := accessTo(f.bob, aliceFiles); a != "rw" || n != 1 {
		t.Fatalf("direct rw + group ro: %q x%d", a, n)
	}
	sharedRoots, err := f.eng.SharedRoots(context.Background(), f.bob.dev.UserID, "files")
	if err != nil || len(sharedRoots) != 1 || sharedRoots[0].Access != "rw" {
		t.Fatalf("shared roots %+v %v", sharedRoots, err)
	}
	f.alice.do(http.MethodDelete, "/api/v1/collections/"+aliceFiles+"/shares?username=bob", nil)

	// Leaving the group ends access.
	f.admin.json(http.MethodPatch, "/api/v1/admin/groups/"+gid, map[string]any{"members": []string{}}, http.StatusOK)
	if a, _ := accessTo(f.bob, aliceFiles); a != "" {
		t.Fatalf("after leaving group: %q", a)
	}

	// Deleting the group removes its shares.
	f.admin.json(http.MethodPatch, "/api/v1/admin/groups/"+gid, map[string]any{"members": []string{f.bob.dev.UserID}, "name": "Crew"}, http.StatusOK)
	if a, _ := accessTo(f.bob, aliceFiles); a != "ro" {
		t.Fatalf("rejoined: %q", a)
	}
	if code, _ := f.admin.do(http.MethodDelete, "/api/v1/admin/groups/"+gid, nil); code != http.StatusNoContent {
		t.Fatalf("delete group %d", code)
	}
	if a, _ := accessTo(f.bob, aliceFiles); a != "" {
		t.Fatalf("after group delete: %q", a)
	}
	left := f.alice.json(http.MethodGet, "/api/v1/collections/"+aliceFiles+"/shares", nil, http.StatusOK)["shares"].([]any)
	if len(left) != 0 {
		t.Fatalf("shares left %v", left)
	}
}
