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
