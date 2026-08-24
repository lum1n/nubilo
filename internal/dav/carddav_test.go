package dav_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/identity"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

const testCard = `BEGIN:VCARD
VERSION:3.0
UID:test-uid-1
FN:Ada Lovelace
N:Lovelace;Ada;;;
EMAIL:ada@example.com
END:VCARD
`

func cardServer(t *testing.T) (*httptest.Server, *identity.Device, string, *identity.Device, string, *syncengine.Engine) {
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
	if _, err := eng.EnsureNamedCollection(context.Background(), "addressbook", "Contacts"); err != nil {
		t.Fatal(err)
	}
	idsvc := identity.NewService(st)
	cardDev, cardPass, err := idsvc.CreateDAVDevice(context.Background(), "iphone-card", "carddav")
	if err != nil {
		t.Fatal(err)
	}
	fileDev, filePass, err := idsvc.CreateDAVDevice(context.Background(), "iphone-files", "webdav")
	if err != nil {
		t.Fatal(err)
	}
	auth := dav.NewAuth(idsvc)
	davH := auth.Middleware(dav.LockCompat(&webdav.Handler{FileSystem: dav.NewFS(eng, st)}))
	cardH := auth.Middleware(&carddav.Handler{Backend: dav.NewCardDAV(eng, st), Prefix: dav.CardDAVPrefix})
	mux := http.NewServeMux()
	mux.Handle("/dav/", davH)
	mux.Handle("/dav", davH)
	mux.Handle("/carddav/", cardH)
	mux.Handle("/carddav", cardH)
	mux.HandleFunc("GET /.well-known/carddav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dav.CardDAVPrefix+"/user/", http.StatusPermanentRedirect)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, cardDev, cardPass, fileDev, filePass, eng
}

func cardReq(t *testing.T, ts *httptest.Server, method, path, user, pass, contentType string, body []byte) *http.Response {
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

func putCard(t *testing.T, ts *httptest.Server, user, pass, href, vcf string) *http.Response {
	t.Helper()
	return cardReq(t, ts, http.MethodPut, href, user, pass, "text/vcard", []byte(vcf))
}

func TestUnauthenticatedCardDAV(t *testing.T) {
	ts, _, _, _, _, _ := cardServer(t)
	resp := cardReq(t, ts, "PROPFIND", "/carddav/", "", "", "application/xml", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing www-authenticate")
	}
}

func TestWellKnownCardDAV(t *testing.T) {
	ts, _, _, _, _, _ := cardServer(t)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Get(ts.URL + "/.well-known/carddav")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("%d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/carddav/user/" {
		t.Fatalf("location %q", loc)
	}
}

func TestWebDAVPasswordCannotCardDAV(t *testing.T) {
	ts, _, _, fileDev, filePass, _ := cardServer(t)
	resp := cardReq(t, ts, "PROPFIND", "/carddav/", fileDev.ID, filePass, "application/xml", []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("webdav-only got %d", resp.StatusCode)
	}
}

func TestCardDAVPasswordCannotWebDAV(t *testing.T) {
	ts, cardDev, cardPass, _, _, _ := cardServer(t)
	resp := cardReq(t, ts, "PROPFIND", "/dav/files", cardDev.ID, cardPass, "application/xml", []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("carddav-only got %d", resp.StatusCode)
	}
}

func TestCardDAVPutGetDelete(t *testing.T) {
	ts, dev, pass, _, _, _ := cardServer(t)
	href := "/carddav/user/addressbooks/Contacts/test-uid-1.vcf"
	resp := putCard(t, ts, dev.ID, pass, href, testCard)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("missing etag")
	}
	resp = cardReq(t, ts, http.MethodGet, href, dev.ID, pass, "", nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get %d", resp.StatusCode)
	}
	if !bytes.Contains(got, []byte("UID:test-uid-1")) || !bytes.Contains(got, []byte("FN:Ada Lovelace")) {
		t.Fatalf("vcard %s", got)
	}
	resp = cardReq(t, ts, http.MethodDelete, href, dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	resp = cardReq(t, ts, http.MethodGet, href, dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete %d", resp.StatusCode)
	}
}

func TestCardDAVUIDConflict(t *testing.T) {
	ts, dev, pass, _, _, _ := cardServer(t)
	resp := putCard(t, ts, dev.ID, pass, "/carddav/user/addressbooks/Contacts/a.vcf", testCard)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("first put %d", resp.StatusCode)
	}
	resp = putCard(t, ts, dev.ID, pass, "/carddav/user/addressbooks/Contacts/b.vcf", testCard)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("uid conflict %d %s", resp.StatusCode, b)
	}
}

func TestCardDAVQueryAndMultiget(t *testing.T) {
	ts, dev, pass, _, _, _ := cardServer(t)
	href := "/carddav/user/addressbooks/Contacts/test-uid-1.vcf"
	resp := putCard(t, ts, dev.ID, pass, href, testCard)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put %d", resp.StatusCode)
	}

	query := `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <D:getetag/>
    <C:address-data/>
  </D:prop>
  <C:filter>
    <C:prop-filter name="FN">
      <C:text-match>Ada</C:text-match>
    </C:prop-filter>
  </C:filter>
</C:addressbook-query>`
	resp = cardReq(t, ts, "REPORT", "/carddav/user/addressbooks/Contacts", dev.ID, pass, "application/xml", []byte(query))
	qbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("query %d %s", resp.StatusCode, qbody)
	}
	if !bytes.Contains(qbody, []byte("FN:Ada Lovelace")) {
		t.Fatalf("query missing contact %s", qbody)
	}

	miss := `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <C:address-data/>
  </D:prop>
  <C:filter>
    <C:prop-filter name="FN">
      <C:text-match>Nobody</C:text-match>
    </C:prop-filter>
  </C:filter>
</C:addressbook-query>`
	resp = cardReq(t, ts, "REPORT", "/carddav/user/addressbooks/Contacts", dev.ID, pass, "application/xml", []byte(miss))
	mbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("miss query %d %s", resp.StatusCode, mbody)
	}
	if bytes.Contains(mbody, []byte("FN:Ada Lovelace")) {
		t.Fatalf("contact leaked for non-matching filter %s", mbody)
	}

	multiget := `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <C:address-data/>
  </D:prop>
  <D:href>` + href + `</D:href>
</C:addressbook-multiget>`
	resp = cardReq(t, ts, "REPORT", "/carddav/user/addressbooks/Contacts", dev.ID, pass, "application/xml", []byte(multiget))
	gbody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		t.Fatalf("multiget %d %s", resp.StatusCode, gbody)
	}
	if !bytes.Contains(gbody, []byte("UID:test-uid-1")) {
		t.Fatalf("multiget missing uid %s", gbody)
	}
}

func TestCardDAVPropfindHome(t *testing.T) {
	ts, dev, pass, _, _, _ := cardServer(t)
	body := []byte(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:resourcetype/></D:prop></D:propfind>`)
	r, _ := http.NewRequest("PROPFIND", ts.URL+"/carddav/user/addressbooks/", bytes.NewReader(body))
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
	if !bytes.Contains(b, []byte("Contacts")) {
		t.Fatalf("missing address book %s", b)
	}
}

func TestCardDAVPathTraversal(t *testing.T) {
	ts, dev, pass, _, _, _ := cardServer(t)
	resp := cardReq(t, ts, http.MethodGet, "/carddav/user/addressbooks/Contacts/../../etc/passwd", dev.ID, pass, "", nil)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("traversal succeeded")
	}
	resp = putCard(t, ts, dev.ID, pass, "/carddav/user/addressbooks/Contacts/%2e%2e/%2e%2e/etc/passwd", "BEGIN:VCARD\nVERSION:3.0\nUID:x\nFN:x\nEND:VCARD\n")
	resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		t.Fatal("encoded traversal put")
	}
}

func TestCardDAVObjectIDNotInHref(t *testing.T) {
	ts, dev, pass, _, _, eng := cardServer(t)
	resp := putCard(t, ts, dev.ID, pass, "/carddav/user/addressbooks/Contacts/test-uid-1.vcf", testCard)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put %d", resp.StatusCode)
	}
	cols, _ := eng.ChildCollections(context.Background(), "addressbook", "")
	if len(cols) != 1 {
		t.Fatalf("books %d", len(cols))
	}
	objs, err := eng.ListObjects(context.Background(), cols[0].ID)
	if err != nil || len(objs) != 1 {
		t.Fatalf("objects %v %v", objs, err)
	}
	meta := dav.ParseContactMeta(objs[0].Metadata)
	if meta.UID != "test-uid-1" || meta.Name != "test-uid-1.vcf" {
		t.Fatalf("meta %+v", meta)
	}
	if objs[0].ID == "test-uid-1" || objs[0].ID == "test-uid-1.vcf" {
		t.Fatal("uid used as object id")
	}
}
