package dav_test

import (
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

type davFamily struct {
	ts             *httptest.Server
	eng            *syncengine.Engine
	alice, bob     *identity.User
	alicePW, bobPW string
	aliceCal       *syncengine.Collection
	aliceFiles     *syncengine.Collection
}

func newDAVFamily(t *testing.T) *davFamily {
	t.Helper()
	ncrypto.Argon2Memory = 8
	ncrypto.Argon2Time = 1
	dir := t.TempDir()
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), make([]byte, 32), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	eng := syncengine.New(st)
	idsvc := identity.NewService(st)
	idsvc.OnUserCreated = func(ctx context.Context, u *identity.User) error { return eng.EnsureUserDefaults(ctx, u.ID) }
	f := &davFamily{eng: eng}
	mk := func(name string) (*identity.User, string) {
		u, err := idsvc.CreateUser(ctx, name, "", identity.UserMember)
		if err != nil {
			t.Fatal(err)
		}
		_, pw, err := idsvc.CreateAppPassword(ctx, u.ID, "iPhone", "all")
		if err != nil {
			t.Fatal(err)
		}
		return u, pw
	}
	f.alice, f.alicePW = mk("alice")
	f.bob, f.bobPW = mk("bob")
	f.aliceCal, _ = eng.FindChildCollection(ctx, f.alice.ID, "calendar", "", "Personal")
	f.aliceFiles, _ = eng.FindChildCollection(ctx, f.alice.ID, "files", "", "Files")
	auth := dav.NewAuth(idsvc)
	mux := http.NewServeMux()
	cal := auth.Middleware(dav.WrapCalDAV(dav.NewCalDAV(eng, st)))
	files := auth.Middleware(dav.LockCompat(&webdav.Handler{FileSystem: dav.NewFS(eng, st)}))
	mux.Handle("/caldav/", cal)
	mux.Handle("/dav/", files)
	mux.Handle("/.well-known/caldav", auth.Middleware(dav.WellKnown(dav.CalDAVPrefix, eng, cal)))
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.ts.Close)
	return f
}

func (f *davFamily) do(t *testing.T, method, path, user, pass, body string, hdr ...string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.SetBasicAuth(user, pass)
	if body != "" && strings.HasPrefix(body, "<?xml") {
		r.Header.Set("Content-Type", "application/xml")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const propfindAll = `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:displayname/><d:current-user-privilege-set/><d:resourcetype/></d:prop></d:propfind>`

func TestCalDAVPerUserPrincipal(t *testing.T) {
	f := newDAVFamily(t)
	code, body := f.do(t, "PROPFIND", "/caldav/u/alice/", "alice", f.alicePW, `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:current-user-principal/><c:calendar-home-set/></d:prop></d:propfind>`, "Depth", "0")
	if code != http.StatusMultiStatus || !strings.Contains(body, "/caldav/u/alice/calendars/") {
		t.Fatalf("principal %d %s", code, body)
	}
	if code, _ := f.do(t, "PROPFIND", "/caldav/u/alice/calendars/", "bob", f.bobPW, propfindAll, "Depth", "1"); code != http.StatusForbidden {
		t.Fatalf("bob read alice's home: %d", code)
	}
	href := "/caldav/u/bob/calendars/" + f.aliceCal.ID + "/x.ics"
	if code, _ := f.do(t, http.MethodPut, href, "bob", f.bobPW, testEventICS, "Content-Type", "text/calendar"); code != http.StatusConflict && code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("bob wrote alice's calendar by id: %d", code)
	}
}

func TestCalDAVSharedCalendar(t *testing.T) {
	f := newDAVFamily(t)
	ctx := context.Background()
	put := "/caldav/u/alice/calendars/" + f.aliceCal.ID + "/test-uid-1.ics"
	if code, body := f.do(t, http.MethodPut, put, "alice", f.alicePW, testEventICS, "Content-Type", "text/calendar"); code >= 300 {
		t.Fatalf("alice put %d %s", code, body)
	}
	if err := f.eng.SetShare(ctx, f.aliceCal.ID, "user", f.bob.ID, "ro", f.alice.ID); err != nil {
		t.Fatal(err)
	}
	code, body := f.do(t, "PROPFIND", "/caldav/u/bob/calendars/", "bob", f.bobPW, propfindAll, "Depth", "1")
	if code != http.StatusMultiStatus {
		t.Fatalf("bob home %d %s", code, body)
	}
	sharedHref := "/caldav/u/bob/calendars/" + f.aliceCal.ID
	i := strings.Index(body, sharedHref)
	if i < 0 || !strings.Contains(body, "Personal (alice)") {
		t.Fatalf("shared calendar missing from bob's home: %s", body)
	}
	block := body[i:]
	if j := strings.Index(strings.ToLower(block), "</d:response>"); j > 0 {
		block = block[:j]
	}
	if strings.Contains(block, "<write") || strings.Contains(block, ":write") || !strings.Contains(block, "read") {
		t.Fatalf("read-only share must advertise read only: %s", block)
	}
	// Bob reads the event through his own home.
	if code, body := f.do(t, http.MethodGet, sharedHref+"/test-uid-1.ics", "bob", f.bobPW, ""); code != http.StatusOK || !strings.Contains(body, "Hello") {
		t.Fatalf("bob get %d %s", code, body)
	}
	// …but cannot write or delete it, nor delete the calendar.
	if code, _ := f.do(t, http.MethodPut, sharedHref+"/bob.ics", "bob", f.bobPW, strings.Replace(testEventICS, "test-uid-1", "bob-1", 1), "Content-Type", "text/calendar"); code != http.StatusForbidden {
		t.Fatalf("ro put %d", code)
	}
	if code, _ := f.do(t, http.MethodDelete, sharedHref+"/test-uid-1.ics", "bob", f.bobPW, ""); code != http.StatusForbidden {
		t.Fatalf("ro delete %d", code)
	}
	// Bob's color choice is his own preference, not Alice's metadata.
	patch := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:a="http://apple.com/ns/ical/"><d:set><d:prop><a:calendar-color>#00FF00</a:calendar-color></d:prop></d:set></d:propertyupdate>`
	if code, _ := f.do(t, "PROPPATCH", sharedHref+"/", "bob", f.bobPW, patch); code != http.StatusMultiStatus {
		t.Fatalf("proppatch %d", code)
	}
	col, _ := f.eng.GetCollection(ctx, f.aliceCal.ID)
	if strings.Contains(string(col.Metadata), "00FF00") {
		t.Fatal("bob's color leaked into alice's calendar")
	}
	if p, _ := f.eng.GetPref(ctx, f.bob.ID, f.aliceCal.ID); p.Color != "#00FF00FF" && p.Color != "#00FF00" {
		t.Fatalf("bob pref %+v", p)
	}
	// Read-write share allows edits, still not deleting the calendar.
	if err := f.eng.SetShare(ctx, f.aliceCal.ID, "user", f.bob.ID, "rw", f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.do(t, http.MethodPut, sharedHref+"/bob.ics", "bob", f.bobPW, strings.Replace(testEventICS, "test-uid-1", "bob-1", 1), "Content-Type", "text/calendar"); code >= 300 {
		t.Fatalf("rw put %d", code)
	}
	if code, _ := f.do(t, http.MethodDelete, sharedHref+"/", "bob", f.bobPW, ""); code != http.StatusForbidden {
		t.Fatalf("rw share deleted the calendar: %d", code)
	}
}

func TestCalDAVMkcalendarKeepsClientPath(t *testing.T) {
	f := newDAVFamily(t)
	body := `<?xml version="1.0"?><c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:set><d:prop><d:displayname>Work</d:displayname></d:prop></d:set></c:mkcalendar>`
	path := "/caldav/u/alice/calendars/2F6A1B7C-0D7E-4C8E-9C1A-3B5E7A9D1F00/"
	if code, out := f.do(t, "MKCALENDAR", path, "alice", f.alicePW, body); code != http.StatusCreated {
		t.Fatalf("mkcalendar %d %s", code, out)
	}
	if code, out := f.do(t, "PROPFIND", path, "alice", f.alicePW, propfindAll, "Depth", "0"); code != http.StatusMultiStatus || !strings.Contains(out, "Work") {
		t.Fatalf("created calendar not at client path: %d %s", code, out)
	}
	if code, out := f.do(t, http.MethodPut, path+"e.ics", "alice", f.alicePW, testEventICS, "Content-Type", "text/calendar"); code >= 300 {
		t.Fatalf("put into new calendar %d %s", code, out)
	}
}

func TestWebDAVSharedFiles(t *testing.T) {
	f := newDAVFamily(t)
	if code, _ := f.do(t, http.MethodPut, "/dav/files/Files/plan.txt", "alice", f.alicePW, "hello"); code >= 300 {
		t.Fatalf("alice put %d", code)
	}
	if code, _ := f.do(t, "PROPFIND", "/dav/shared/alice/", "bob", f.bobPW, "", "Depth", "1"); code != http.StatusNotFound {
		t.Fatalf("unshared tree visible: %d", code)
	}
	if err := f.eng.SetShare(context.Background(), f.aliceFiles.ID, "user", f.bob.ID, "ro", f.alice.ID); err != nil {
		t.Fatal(err)
	}
	code, body := f.do(t, "PROPFIND", "/dav/shared/", "bob", f.bobPW, "", "Depth", "1")
	if code != http.StatusMultiStatus || !strings.Contains(body, "/dav/shared/alice") {
		t.Fatalf("shared listing %d %s", code, body)
	}
	if code, body := f.do(t, http.MethodGet, "/dav/shared/alice/Files/plan.txt", "bob", f.bobPW, ""); code != http.StatusOK || body != "hello" {
		t.Fatalf("bob read %d %q", code, body)
	}
	if code, _ := f.do(t, http.MethodPut, "/dav/shared/alice/Files/bob.txt", "bob", f.bobPW, "x"); code != http.StatusForbidden {
		t.Fatalf("ro put %d", code)
	}
	if code, _ := f.do(t, "MKCOL", "/dav/shared/alice/New", "bob", f.bobPW, ""); code != http.StatusForbidden {
		t.Fatalf("mkcol in other's space %d", code)
	}
	if err := f.eng.SetShare(context.Background(), f.aliceFiles.ID, "user", f.bob.ID, "rw", f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.do(t, http.MethodPut, "/dav/shared/alice/Files/bob.txt", "bob", f.bobPW, "x"); code >= 300 {
		t.Fatalf("rw put %d", code)
	}
	if code, _ := f.do(t, "MOVE", "/dav/shared/alice/Files/plan.txt", "bob", f.bobPW, "", "Destination", f.ts.URL+"/dav/files/Files/plan.txt"); code != http.StatusForbidden {
		t.Fatalf("moving alice's file into bob's space: %d", code)
	}
	if code, _ := f.do(t, http.MethodDelete, "/dav/shared/alice/Files/", "bob", f.bobPW, ""); code != http.StatusForbidden {
		t.Fatalf("rw share deleted the root: %d", code)
	}
	if code, body := f.do(t, http.MethodGet, "/dav/files/Files/bob.txt", "alice", f.alicePW, ""); code != http.StatusOK || body != "x" {
		t.Fatalf("alice sees bob's upload %d %q", code, body)
	}
}

func TestUsernameBasicAuthAndWellKnown(t *testing.T) {
	f := newDAVFamily(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/.well-known/caldav", nil)
	r.SetBasicAuth("bob", f.bobPW)
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/caldav/u/bob/" {
		t.Fatalf("well-known location %q", loc)
	}
	if code, _ := f.do(t, "PROPFIND", "/caldav/u/bob/", "bob", f.alicePW, propfindAll, "Depth", "0"); code != http.StatusUnauthorized {
		t.Fatalf("alice's password accepted for bob: %d", code)
	}
}

func TestSharedSlugCannotShadowOwnCalendar(t *testing.T) {
	f := newDAVFamily(t)
	ctx := context.Background()
	trap, err := f.eng.CreateCollection(ctx, f.alice.ID, "calendar", "Family", "", []byte(`{"slug":"Personal"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.SetShare(ctx, trap.ID, "user", f.bob.ID, "rw", f.alice.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/caldav/user/calendars/Personal/x.ics", "/caldav/u/bob/calendars/Personal/y.ics"} {
		ics := strings.Replace(testEventICS, "test-uid-1", "uid-"+p[len(p)-5:len(p)-4], 1)
		if code, body := f.do(t, http.MethodPut, p, "bob", f.bobPW, ics, "Content-Type", "text/calendar"); code >= 300 {
			t.Fatalf("put %s %d %s", p, code, body)
		}
	}
	if objs, _ := f.eng.ListObjects(ctx, trap.ID); len(objs) != 0 {
		t.Fatalf("bob's events landed in alice's calendar: %d", len(objs))
	}
}
