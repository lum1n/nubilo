package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"nubilo/internal/identity"
	"nubilo/internal/syncengine"
	"nubilo/internal/update"
)

func TestUpdateAPI(t *testing.T) {
	ts, idsvc, st, srv := startServer(t)
	eng := syncengine.New(st)
	idsvc.OnUserCreated = func(ctx context.Context, u *identity.User) error { return eng.EnsureUserDefaults(ctx, u.ID) }
	f := &familyServer{ts: ts, ids: idsvc, st: st, eng: eng}
	admin := f.user(t, "mum", identity.UserAdmin)
	member := f.user(t, "kid", identity.UserMember)

	// A server without the updater: the apps hide the card on 404.
	if code, _ := admin.do(http.MethodGet, "/api/v1/admin/update", nil); code != http.StatusNotFound {
		t.Fatalf("no updater: %d", code)
	}

	releases := httptest.NewServer(http.NotFoundHandler()) // nothing published yet
	t.Cleanup(releases.Close)
	srv.Updater = &update.Manager{
		DataDir: t.TempDir(), Current: "0.8.0",
		Source: update.Source{ManifestURL: releases.URL + "/manifest.json", Keys: nil, HTTP: releases.Client()},
	}

	if code, _ := member.do(http.MethodGet, "/api/v1/admin/update", nil); code != http.StatusForbidden {
		t.Fatalf("member: %d", code)
	}
	st0 := admin.json(http.MethodGet, "/api/v1/admin/update", nil, http.StatusOK)
	if st0["current"] != "0.8.0" || st0["auto"] != true || st0["state"] != "idle" || st0["available"] != nil {
		t.Fatalf("status %v", st0)
	}
	checked := admin.json(http.MethodPost, "/api/v1/admin/update/check", nil, http.StatusOK)
	if checked["available"] != nil || checked["checked_at"] == float64(0) {
		t.Fatalf("check %v", checked)
	}
	if code, out := admin.do(http.MethodPost, "/api/v1/admin/update/install", nil); code != http.StatusConflict {
		t.Fatalf("install with nothing available: %d %s", code, out)
	}
	off := admin.json(http.MethodPut, "/api/v1/admin/update/settings", map[string]bool{"auto": false}, http.StatusOK)
	if off["auto"] != false || off["next_auto_at"] != float64(0) {
		t.Fatalf("settings %v", off)
	}
	if code, _ := admin.do(http.MethodPut, "/api/v1/admin/update/settings", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("empty settings: %d", code)
	}
}
