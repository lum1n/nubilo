package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestFolderEndpoints(t *testing.T) {
	f := startFamily(t)
	ctx := context.Background()
	alice, bob := f.alice, f.bob
	files := f.files(t, alice)

	docs := alice.json(http.MethodPost, "/sync/v1/collection", map[string]any{"kind": "files", "name": "Docs", "parent_id": files}, http.StatusOK)
	docsID, _ := docs["id"].(string)
	if code, out := alice.do(http.MethodPost, "/sync/v1/objects", map[string]any{"collection_id": docsID}); code != http.StatusOK || !strings.Contains(string(out), `"objects":[]`) {
		t.Fatalf("empty folder: %d %s", code, out)
	}
	_, hash := putBlob(alice, []byte("hello"))
	if st := pushStatus(pushCreate(alice, docsID, hash, 5)); st != "ok" {
		t.Fatalf("push %s", st)
	}

	list := alice.json(http.MethodPost, "/sync/v1/objects", map[string]any{"collection_id": docsID}, http.StatusOK)
	if objs, _ := list["objects"].([]any); len(objs) != 1 {
		t.Fatalf("objects %v", list)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/objects", map[string]any{"collection_id": docsID}); code != http.StatusNotFound {
		t.Fatalf("bob listed alice's folder: %d", code)
	}
	objs, _ := list["objects"].([]any)
	objID, _ := objs[0].(map[string]any)["id"].(string)
	one := alice.json(http.MethodPost, "/sync/v1/object", map[string]any{"id": objID}, http.StatusOK)
	if one["collection_id"] != docsID {
		t.Fatalf("object %v", one)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/object", map[string]any{"id": objID}); code != http.StatusNotFound {
		t.Fatalf("bob read alice's object: %d", code)
	}

	// Rename, then a cycle is refused and a move to the top level works.
	alice.json(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "name": "Papers"}, http.StatusOK)
	inner := alice.json(http.MethodPost, "/sync/v1/collection", map[string]any{"kind": "files", "name": "Inner", "parent_id": docsID}, http.StatusOK)
	innerID, _ := inner["id"].(string)
	if code, _ := alice.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "parent_id": innerID}); code != http.StatusConflict {
		t.Fatalf("cycle allowed: %d", code)
	}
	moved := alice.json(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": innerID, "parent_id": ""}, http.StatusOK)
	if p, _ := moved["parent_id"].(string); p != "" {
		t.Fatalf("not top-level: %v", moved)
	}
	// A name already used by a sibling is refused.
	if code, _ := alice.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": innerID, "name": "Files"}); code != http.StatusConflict {
		t.Fatalf("duplicate name allowed: %d", code)
	}

	// Others cannot delete; the owner deletes the folder with its files.
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/delete", map[string]any{"id": docsID}); code != http.StatusNotFound {
		t.Fatalf("bob deleted alice's folder: %d", code)
	}
	if code, out := alice.do(http.MethodPost, "/sync/v1/collection/delete", map[string]any{"id": docsID}); code != http.StatusNoContent {
		t.Fatalf("delete %d %s", code, out)
	}
	if objs, _ := f.eng.ListObjects(ctx, docsID); len(objs) != 0 {
		t.Fatalf("objects left: %d", len(objs))
	}
	if c, err := f.eng.GetCollection(ctx, docsID); err != nil || c.DeletedAt == nil {
		t.Fatalf("folder not deleted: %v %v", c, err)
	}
}

func TestFolderEndpointsPaging(t *testing.T) {
	f := startFamily(t)
	alice := f.alice
	files := f.files(t, alice)
	docs := alice.json(http.MethodPost, "/sync/v1/collection", map[string]any{"kind": "files", "name": "Docs", "parent_id": files}, http.StatusOK)
	docsID, _ := docs["id"].(string)
	for i := 0; i < 3; i++ {
		_, hash := putBlob(alice, []byte{byte('a' + i)})
		if st := pushStatus(pushCreate(alice, docsID, hash, 1)); st != "ok" {
			t.Fatalf("push %s", st)
		}
	}

	seen := map[string]bool{}
	after := ""
	for page := 0; ; page++ {
		if page > 3 {
			t.Fatal("paging does not end")
		}
		list := alice.json(http.MethodPost, "/sync/v1/objects", map[string]any{"collection_id": docsID, "after_id": after, "limit": 2}, http.StatusOK)
		objs, _ := list["objects"].([]any)
		for _, o := range objs {
			id, _ := o.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("object %s listed twice", id)
			}
			seen[id] = true
		}
		next, _ := list["next_after_id"].(string)
		if len(objs) == 2 && next == "" || len(objs) < 2 && next != "" {
			t.Fatalf("page %d: %d objects, next %q", page, len(objs), next)
		}
		if next == "" {
			break
		}
		after = next
	}
	if len(seen) != 3 {
		t.Fatalf("listed %d objects, want 3", len(seen))
	}

	// A deleted object is gone; bad requests are refused.
	var id string
	for k := range seen {
		id = k
		break
	}
	obj := alice.json(http.MethodPost, "/sync/v1/object", map[string]any{"id": id}, http.StatusOK)
	rev, _ := obj["revision"].(float64)
	del := alice.json(http.MethodPost, "/sync/v1/push", map[string]any{
		"idempotency_key": nonce(),
		"changes":         []map[string]any{{"object_id": id, "collection_id": docsID, "op": "delete", "base_revision": rev}},
	}, http.StatusOK)
	if st := pushStatus(del); st != "ok" {
		t.Fatalf("delete %s", st)
	}
	if code, _ := alice.do(http.MethodPost, "/sync/v1/object", map[string]any{"id": id}); code != http.StatusNotFound {
		t.Fatalf("deleted object: %d", code)
	}
	for path, body := range map[string]map[string]any{
		"/sync/v1/objects":           {},
		"/sync/v1/object":            {},
		"/sync/v1/collection/update": {},
		"/sync/v1/collection/delete": {},
	} {
		if code, _ := alice.do(http.MethodPost, path, body); code != http.StatusBadRequest {
			t.Fatalf("%s without id: %d", path, code)
		}
	}
	if code, _ := alice.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": "nope", "name": "X"}); code != http.StatusNotFound {
		t.Fatalf("update missing folder: %d", code)
	}
	if code, _ := alice.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "name": "a/b"}); code != http.StatusBadRequest {
		t.Fatalf("bad name allowed: %d", code)
	}
}

func TestFolderEndpointsShared(t *testing.T) {
	f := startFamily(t)
	ctx := context.Background()
	alice, bob := f.alice, f.bob
	aliceFiles, bobFiles := f.files(t, alice), f.files(t, bob)
	docs := alice.json(http.MethodPost, "/sync/v1/collection", map[string]any{"kind": "files", "name": "Docs", "parent_id": aliceFiles}, http.StatusOK)
	docsID, _ := docs["id"].(string)

	// Read-only: Bob lists but cannot rename or delete.
	alice.json(http.MethodPut, "/api/v1/collections/"+aliceFiles+"/shares", map[string]any{"username": "bob", "access": "ro"}, http.StatusOK)
	bob.json(http.MethodPost, "/sync/v1/objects", map[string]any{"collection_id": docsID}, http.StatusOK)
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "name": "Mine"}); code != http.StatusForbidden {
		t.Fatalf("ro rename: %d", code)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/delete", map[string]any{"id": docsID}); code != http.StatusForbidden {
		t.Fatalf("ro delete: %d", code)
	}

	// Read-write: Bob renames subfolders but not the shared top-level folder,
	// and nothing moves between Alice and Bob.
	alice.json(http.MethodPut, "/api/v1/collections/"+aliceFiles+"/shares", map[string]any{"username": "bob", "access": "rw"}, http.StatusOK)
	renamed := bob.json(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "name": "Shared docs"}, http.StatusOK)
	if renamed["name"] != "Shared docs" {
		t.Fatalf("rename %v", renamed)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": aliceFiles, "name": "Bob's now"}); code != http.StatusForbidden {
		t.Fatalf("rw renamed top-level: %d", code)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/delete", map[string]any{"id": aliceFiles}); code != http.StatusForbidden {
		t.Fatalf("rw deleted top-level: %d", code)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "parent_id": ""}); code != http.StatusForbidden {
		t.Fatalf("moved alice's folder to bob's top level: %d", code)
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": docsID, "parent_id": bobFiles}); code != http.StatusForbidden {
		t.Fatalf("moved alice's folder into bob's: %d", code)
	}
	bobs := bob.json(http.MethodPost, "/sync/v1/collection", map[string]any{"kind": "files", "name": "Bob's", "parent_id": bobFiles}, http.StatusOK)
	bobsID, _ := bobs["id"].(string)
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/update", map[string]any{"id": bobsID, "parent_id": docsID}); code != http.StatusForbidden {
		t.Fatalf("moved bob's folder into alice's: %d", code)
	}
	if c, err := f.eng.GetCollection(ctx, docsID); err != nil || c.ParentID != aliceFiles {
		t.Fatalf("docs moved: %v %v", c, err)
	}

	// Bob deletes a shared subfolder with a nested folder and their files.
	inner := alice.json(http.MethodPost, "/sync/v1/collection", map[string]any{"kind": "files", "name": "Inner", "parent_id": docsID}, http.StatusOK)
	innerID, _ := inner["id"].(string)
	_, hash := putBlob(alice, []byte("deep"))
	if st := pushStatus(pushCreate(alice, innerID, hash, 4)); st != "ok" {
		t.Fatalf("push %s", st)
	}
	if code, out := bob.do(http.MethodPost, "/sync/v1/collection/delete", map[string]any{"id": docsID}); code != http.StatusNoContent {
		t.Fatalf("rw delete %d %s", code, out)
	}
	if objs, _ := f.eng.ListObjects(ctx, innerID); len(objs) != 0 {
		t.Fatalf("nested objects left: %d", len(objs))
	}
	for _, id := range []string{docsID, innerID} {
		if c, err := f.eng.GetCollection(ctx, id); err != nil || c.DeletedAt == nil {
			t.Fatalf("folder %s not deleted: %v %v", id, c, err)
		}
	}
	if code, _ := bob.do(http.MethodPost, "/sync/v1/collection/delete", map[string]any{"id": docsID}); code != http.StatusNotFound {
		t.Fatalf("deleted twice: %d", code)
	}
}
