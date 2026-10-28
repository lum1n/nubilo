package server_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

func TestQuotaAPI(t *testing.T) {
	f := startFamily(t)
	// Members cannot set quotas; admins can.
	if code, _ := f.alice.do(http.MethodPatch, "/api/v1/admin/users/"+f.alice.dev.UserID, map[string]any{"quota_bytes": 0}); code != http.StatusForbidden {
		t.Fatalf("member set quota: %d", code)
	}
	u := f.admin.json(http.MethodPatch, "/api/v1/admin/users/"+f.alice.dev.UserID, map[string]any{"quota_bytes": 8}, http.StatusOK)
	if u["quota_bytes"].(float64) != 8 {
		t.Fatalf("patch %v", u)
	}
	if code, _ := f.admin.do(http.MethodPatch, "/api/v1/admin/users/"+f.alice.dev.UserID, map[string]any{"quota_bytes": -1}); code != http.StatusBadRequest {
		t.Fatalf("negative quota: %d", code)
	}
	// A patch without quota_bytes leaves it.
	f.admin.json(http.MethodPatch, "/api/v1/admin/users/"+f.alice.dev.UserID, map[string]any{"display_name": "Alice"}, http.StatusOK)

	files := f.files(t, f.alice)
	_, h := putBlob(f.alice, []byte("12345"))
	if s := pushStatus(pushCreate(f.alice, files, h, 5)); s != "ok" {
		t.Fatalf("within quota: %s", s)
	}
	_, h = putBlob(f.alice, []byte("abcde"))
	if s := pushStatus(pushCreate(f.alice, files, h, 5)); s != "error:quota exceeded" {
		t.Fatalf("past quota: %s", s)
	}
	me := f.alice.json(http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	st := me["storage"].(map[string]any)
	if st["quota_bytes"].(float64) != 8 || st["bytes"].(float64) != 5 {
		t.Fatalf("me storage %v", st)
	}

	// WebDAV answers 507 so Finder and Files show "not enough space".
	dav, pass, err := f.ids.CreateAppPassword(context.Background(), f.alice.dev.UserID, "files", "webdav")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, f.ts.URL+"/dav/files/Files/big.txt", bytes.NewReader([]byte("too big for it")))
	req.SetBasicAuth(dav.ID, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("dav put past quota: %d", resp.StatusCode)
	}
}
