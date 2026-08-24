package dav_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/identity"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

const testEventICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//nubilo//test//EN
BEGIN:VEVENT
UID:test-uid-1
DTSTAMP:20260101T000000Z
DTSTART:20260824T100000Z
DTEND:20260824T110000Z
SUMMARY:Hello
END:VEVENT
END:VCALENDAR
`

func calServer(t *testing.T) (*httptest.Server, *identity.Device, string, *identity.Device, string, *syncengine.Engine) {
	t.Helper()
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := syncengine.New(st)
	if _, err := eng.EnsureNamedCollection(context.Background(), "files", "Files"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "calendar", "Personal"); err != nil {
		t.Fatal(err)
	}
	idsvc := identity.NewService(st)
	calDev, calPass, err := idsvc.CreateDAVDevice(context.Background(), "iphone-cal", "caldav")
	if err != nil {
		t.Fatal(err)
	}
	fileDev, filePass, err := idsvc.CreateDAVDevice(context.Background(), "iphone-files", "webdav")
	if err != nil {
		t.Fatal(err)
	}
	auth := dav.NewAuth(idsvc)
	davH := auth.Middleware(dav.LockCompat(&webdav.Handler{FileSystem: dav.NewFS(eng, st)}))
	calH := auth.Middleware(dav.WrapCalDAV(dav.NewCalDAV(eng, st)))
	mux := http.NewServeMux()
	mux.Handle("/dav/", davH)
	mux.Handle("/dav", davH)
	mux.Handle("/caldav/", calH)
	mux.Handle("/caldav", calH)
	mux.Handle("/.well-known/caldav", dav.WellKnown(dav.CalDAVPrefix+"/user/", calH))
	mux.Handle("/.well-known/caldav/", dav.WellKnown(dav.CalDAVPrefix+"/user/", calH))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, calDev, calPass, fileDev, filePass, eng
}

func calReq(t *testing.T, ts *httptest.Server, method, path, user, pass, contentType string, body []byte) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		r.SetBasicAuth(user, pass)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func putEvent(t *testing.T, ts *httptest.Server, user, pass, href, ics string) *http.Response {
	t.Helper()
	return calReq(t, ts, http.MethodPut, href, user, pass, "text/calendar", []byte(ics))
}

func TestUnauthenticatedCalDAV(t *testing.T) {
	ts, _, _, _, _, _ := calServer(t)
	resp := calReq(t, ts, "PROPFIND", "/caldav/", "", "", "application/xml", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing www-authenticate")
	}
}

func TestWellKnownCalDAV(t *testing.T) {
	ts, _, _, _, _, _ := calServer(t)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Get(ts.URL + "/.well-known/caldav")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("%d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/caldav/user/" {
		t.Fatalf("location %q", loc)
	}
}

func TestWebDAVPasswordCannotCalDAV(t *testing.T) {
	ts, _, _, fileDev, filePass, _ := calServer(t)
	resp := calReq(t, ts, "PROPFIND", "/caldav/", fileDev.ID, filePass, "application/xml", []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("webdav-only got %d", resp.StatusCode)
	}
}

func TestCalDAVPasswordCannotWebDAV(t *testing.T) {
	ts, calDev, calPass, _, _, _ := calServer(t)
	resp := calReq(t, ts, "PROPFIND", "/dav/files", calDev.ID, calPass, "application/xml", []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("caldav-only got %d", resp.StatusCode)
	}
}

func TestCalDAVPutGetDelete(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	href := "/caldav/user/calendars/Personal/test-uid-1.ics"
	resp := putEvent(t, ts, dev.ID, pass, href, testEventICS)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("missing etag")
	}
	resp = calReq(t, ts, http.MethodGet, href, dev.ID, pass, "", nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get %d", resp.StatusCode)
	}
	if !bytes.Contains(got, []byte("UID:test-uid-1")) || !bytes.Contains(got, []byte("SUMMARY:Hello")) {
		t.Fatalf("ics %s", got)
	}
	resp = calReq(t, ts, http.MethodDelete, href, dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	resp = calReq(t, ts, http.MethodGet, href, dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete %d", resp.StatusCode)
	}
}

func TestCalDAVUIDConflict(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	resp := putEvent(t, ts, dev.ID, pass, "/caldav/user/calendars/Personal/a.ics", testEventICS)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("first put %d", resp.StatusCode)
	}
	resp = putEvent(t, ts, dev.ID, pass, "/caldav/user/calendars/Personal/b.ics", testEventICS)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("uid conflict %d %s", resp.StatusCode, b)
	}
}

func TestCalDAVQueryAndMultiget(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	href := "/caldav/user/calendars/Personal/test-uid-1.ics"
	resp := putEvent(t, ts, dev.ID, pass, href, testEventICS)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put %d", resp.StatusCode)
	}

	query := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="20260801T000000Z" end="20260901T000000Z"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
	resp = calReq(t, ts, "REPORT", "/caldav/user/calendars/Personal", dev.ID, pass, "application/xml", []byte(query))
	qbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("query %d %s", resp.StatusCode, qbody)
	}
	if !bytes.Contains(qbody, []byte("SUMMARY:Hello")) {
		t.Fatalf("query missing event %s", qbody)
	}

	outside := strings.Replace(query, "20260801T000000Z", "20250101T000000Z", 1)
	outside = strings.Replace(outside, "20260901T000000Z", "20250201T000000Z", 1)
	resp = calReq(t, ts, "REPORT", "/caldav/user/calendars/Personal", dev.ID, pass, "application/xml", []byte(outside))
	obody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("outside query %d %s", resp.StatusCode, obody)
	}
	if bytes.Contains(obody, []byte("SUMMARY:Hello")) {
		t.Fatalf("event leaked outside range %s", obody)
	}

	weekly := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//nubilo//test//EN
BEGIN:VEVENT
UID:weekly-old
DTSTAMP:20200106T000000Z
DTSTART;TZID=Europe/Oslo:20200106T100000
DTEND;TZID=Europe/Oslo:20200106T110000
RRULE:FREQ=WEEKLY;BYDAY=MO
SUMMARY:Old standup
END:VEVENT
END:VCALENDAR
`
	resp = putEvent(t, ts, dev.ID, pass, "/caldav/user/calendars/Personal/weekly-old.ics", weekly)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put weekly %d", resp.StatusCode)
	}
	resp = calReq(t, ts, "REPORT", "/caldav/user/calendars/Personal", dev.ID, pass, "application/xml", []byte(query))
	wbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("weekly query %d %s", resp.StatusCode, wbody)
	}
	if !bytes.Contains(wbody, []byte("Old standup")) {
		t.Fatalf("recurring series missing from current-month query %s", wbody)
	}

	multiget := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data/>
  </D:prop>
  <D:href>` + href + `</D:href>
</C:calendar-multiget>`
	resp = calReq(t, ts, "REPORT", "/caldav/user/calendars/Personal", dev.ID, pass, "application/xml", []byte(multiget))
	mbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("multiget %d %s", resp.StatusCode, mbody)
	}
	if !bytes.Contains(mbody, []byte("UID:test-uid-1")) {
		t.Fatalf("multiget missing uid %s", mbody)
	}
}

func TestCalDAVPropfindHome(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	body := []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:resourcetype/></D:prop></D:propfind>`)
	r, _ := http.NewRequest("PROPFIND", ts.URL+"/caldav/user/calendars/", bytes.NewReader(body))
	r.SetBasicAuth(dev.ID, pass)
	r.Header.Set("Depth", "1")
	r.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("propfind %d %s", resp.StatusCode, b)
	}
	if !bytes.Contains(b, []byte("Personal")) {
		t.Fatalf("missing calendar %s", b)
	}
}

func TestCalDAVPathTraversal(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	resp := calReq(t, ts, http.MethodGet, "/caldav/user/calendars/Personal/../../etc/passwd", dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("traversal succeeded")
	}
	resp = putEvent(t, ts, dev.ID, pass, "/caldav/user/calendars/Personal/%2e%2e/%2e%2e/etc/passwd", "BEGIN:VCALENDAR\nEND:VCALENDAR\n")
	resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		t.Fatal("encoded traversal put")
	}
}

func TestCalDAVObjectIDNotInHref(t *testing.T) {
	ts, dev, pass, _, _, eng := calServer(t)
	resp := putEvent(t, ts, dev.ID, pass, "/caldav/user/calendars/Personal/test-uid-1.ics", testEventICS)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put %d", resp.StatusCode)
	}
	cols, _ := eng.ChildCollections(context.Background(), "calendar", "")
	if len(cols) != 1 {
		t.Fatalf("calendars %d", len(cols))
	}
	objs, err := eng.ListObjects(context.Background(), cols[0].ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("objects %v %v", objs, err)
	}
	meta := dav.ParseEventMeta(objs[0].Metadata)
	if meta.UID != "test-uid-1" || meta.Name != "test-uid-1.ics" {
		t.Fatalf("meta %+v", meta)
	}
	if objs[0].ID == "test-uid-1" || objs[0].ID == "test-uid-1.ics" {
		t.Fatal("uid used as object id")
	}
}

func TestWellKnownCalDAVPropfind(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	body := []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/></D:prop></D:propfind>`)
	r, _ := http.NewRequest("PROPFIND", ts.URL+"/.well-known/caldav", bytes.NewReader(body))
	r.SetBasicAuth(dev.ID, pass)
	r.Header.Set("Depth", "0")
	r.Header.Set("Content-Type", "application/xml")
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("well-known propfind %d %s", resp.StatusCode, b)
	}
	if !bytes.Contains(b, []byte("current-user-principal")) && !bytes.Contains(b, []byte("calendar-home-set")) {
		t.Fatalf("missing principal props %s", b)
	}
}

func TestCalDAVPropPatchAppleColor(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	body := []byte(`<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/">
  <D:set><D:prop><A:calendar-color>#FF0000</A:calendar-color></D:prop></D:set>
</D:propertyupdate>`)
	resp := calReq(t, ts, "PROPPATCH", "/caldav/user/calendars/Personal", dev.ID, pass, "application/xml", body)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 {
		t.Fatalf("proppatch %d %s", resp.StatusCode, b)
	}
	if !bytes.Contains(b, []byte("200 OK")) {
		t.Fatalf("proppatch body %s", b)
	}
}

func TestCalDAVPrincipalSlashRedirect(t *testing.T) {
	ts, dev, pass, _, _, _ := calServer(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	r, _ := http.NewRequest("PROPFIND", ts.URL+"/caldav/user", bytes.NewReader([]byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`)))
	r.SetBasicAuth(dev.ID, pass)
	r.Header.Set("Depth", "0")
	r.Header.Set("Content-Type", "application/xml")
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("%d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasSuffix(loc, "/caldav/user/") {
		t.Fatalf("location %q", loc)
	}
}

func TestDAVResourceName(t *testing.T) {
	got := dav.DAVResourceName("icloud/abc==/def", ".ics")
	if strings.Contains(got, "/") || !strings.HasSuffix(got, ".ics") {
		t.Fatalf("%q", got)
	}
	if dav.DAVResourceName("Work", "") != "Work" {
		t.Fatal("plain name")
	}
}
