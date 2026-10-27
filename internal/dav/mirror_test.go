package dav_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"nubilo/internal/dav"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/syncengine"
)

// A mirrored calendar (copied from the Mac by the agent) is read-only for
// CalDAV clients but still writable by the agent, and can be removed whole.
func TestCalDAVMirrorIsReadOnly(t *testing.T) {
	ts, dev, pass, _, _, eng := calServer(t)
	ctx := context.Background()
	if _, err := eng.EnsureNamedCollection(ctx, dev.UserID, "calendar", "Work"); err != nil {
		t.Fatal(err)
	}
	href := "/caldav/user/calendars/Work/test-uid-1.ics"
	resp := putEvent(t, ts, dev.ID, pass, href, testEventICS)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("seed put %d", resp.StatusCode)
	}
	col, err := eng.FindChildCollection(ctx, dev.UserID, "calendar", "", "Work")
	if err != nil {
		t.Fatal(err)
	}
	// The Mac app turns the mirror on; the server records it as the writer,
	// whatever mirror_device the client sent.
	mac := &identity.Device{ID: "mac-app", Role: identity.RoleClient, UserID: dev.UserID,
		Permissions: identity.DefaultPermissions(identity.RoleClient)}
	patch := syncengine.MirrorPatch(json.RawMessage(`{"mirror":true,"mirror_device":"someone-else"}`), mac)
	if _, err := eng.PatchCollectionMetadata(ctx, col.ID, patch); err != nil {
		t.Fatal(err)
	}

	resp = putEvent(t, ts, dev.ID, pass, href, strings.Replace(testEventICS, "SUMMARY:Hello", "SUMMARY:Edited on the phone", 1))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("phone edit of a mirror: %d, want 403", resp.StatusCode)
	}
	resp = calReq(t, ts, http.MethodDelete, href, dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("phone delete of a mirror event: %d, want 403", resp.StatusCode)
	}

	resp = calReq(t, ts, "PROPFIND", "/caldav/user/calendars/Work/", dev.ID, pass, "application/xml",
		[]byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:current-user-privilege-set/></D:prop></D:propfind>`))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "current-user-privilege-set") || strings.Contains(string(body), "<write") || strings.Contains(string(body), ":write") {
		t.Fatalf("mirror not advertised read-only: %s", body)
	}

	// A color change from the phone keeps the mirror and its writer.
	resp = calReq(t, ts, "PROPPATCH", "/caldav/user/calendars/Work/", dev.ID, pass, "application/xml",
		[]byte(`<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop><A:calendar-color>#FF0000FF</A:calendar-color></D:prop></D:set></D:propertyupdate>`))
	resp.Body.Close()
	got, err := eng.GetCollection(ctx, col.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m := dav.ParseCalendarColMeta(got.Metadata); !m.Mirror || m.MirrorDevice != "mac-app" || m.Color == "" {
		t.Fatalf("after color change %s", got.Metadata)
	}

	// The device that owns the mirror may still write.
	objs, err := eng.ListObjects(ctx, col.ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("objects %d %v", len(objs), err)
	}
	res, err := eng.Push(ctx, mac, ids.New(), []syncengine.ChangeInput{{
		ObjectID: objs[0].ID, CollectionID: col.ID, Op: syncengine.OpDelete, BaseRevision: objs[0].Revision, Force: true,
	}})
	if err != nil || res[0].Status != "ok" {
		t.Fatalf("mirror owner delete %v %+v", err, res)
	}

	// Removing the whole mirrored calendar from the phone works.
	resp = calReq(t, ts, http.MethodDelete, "/caldav/user/calendars/Work/", dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("delete mirrored calendar %d", resp.StatusCode)
	}
	if _, err := eng.FindChildCollection(ctx, dev.UserID, "calendar", "", "Work"); err == nil {
		t.Fatal("mirrored calendar still there")
	}
}
