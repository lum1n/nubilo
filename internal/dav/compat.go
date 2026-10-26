package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"nubilo/internal/syncengine"
)

// WellKnown serves RFC 6764 discovery for the authenticated user (it must
// run inside the auth middleware). GET/HEAD redirect to the user's
// principal; iOS sends PROPFIND, which is served at the principal directly.
func WellKnown(prefix string, eng *syncengine.Engine, dav http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := currentUser(r.Context(), eng)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		principal := prefix + "/" + userSeg + "/" + u.username + "/"
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			http.Redirect(w, r, principal, http.StatusMovedPermanently)
		default:
			r2 := r.Clone(r.Context())
			r2.URL.Path = principal
			r2.URL.RawPath = ""
			dav.ServeHTTP(w, r2)
		}
	})
}

// WrapCalDAV adds Apple Calendar compatibility around go-webdav.
func WrapCalDAV(b *CalDAV) http.Handler {
	return appleDAV{
		prefix:  b.Prefix,
		homeSeg: calHomeSeg,
		inner: func(prefix string) http.Handler {
			return &caldav.Handler{Backend: b, Prefix: prefix}
		},
		mkcalendar: b,
		cal:        b,
	}
}

// WrapCardDAV adds Apple Contacts compatibility around go-webdav.
func WrapCardDAV(b *CardDAV) http.Handler {
	return appleDAV{
		prefix:  b.Prefix,
		homeSeg: cardHomeSeg,
		inner: func(prefix string) http.Handler {
			return &carddav.Handler{Backend: b, Prefix: prefix}
		},
		card: b,
	}
}

type mkCalendarHandler interface {
	createFromPath(w http.ResponseWriter, r *http.Request)
}

type appleDAV struct {
	prefix     string
	homeSeg    string
	inner      func(prefix string) http.Handler
	mkcalendar mkCalendarHandler
	cal        *CalDAV
	card       *CardDAV
}

func (h appleDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if canonicalDAVSlash(w, r, h.containers(r.URL.Path)) {
		return
	}
	if !h.ownHome(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	r = r.WithContext(withLegacyStyle(r.Context(), isLegacyPath(h.prefix, r.URL.Path)))
	next := h.inner(handlerPrefix(h.prefix, r.URL.Path))
	switch r.Method {
	case "PROPPATCH":
		if h.cal != nil {
			h.cal.serveCalendarPropPatch(w, r)
			return
		}
		servePropPatchOK(w, r)
		return
	case "MKCALENDAR":
		if h.mkcalendar != nil {
			h.mkcalendar.createFromPath(w, r)
			return
		}
	case "PROPFIND":
		h.serveCalPropFind(w, r, next)
		return
	}
	next.ServeHTTP(w, r)
}

// ownHome rejects requests under another user's /<prefix>/u/<username>/.
func (h appleDAV) ownHome(r *http.Request) bool {
	segs, err := Split(strings.TrimPrefix(r.URL.Path, h.prefix))
	if err != nil || len(segs) < 2 || segs[0] != userSeg {
		return true
	}
	eng := h.engine()
	if eng == nil {
		return false
	}
	u, err := currentUser(r.Context(), eng)
	return err == nil && strings.EqualFold(segs[1], u.username)
}

func (h appleDAV) engine() *syncengine.Engine {
	if h.cal != nil {
		return h.cal.Engine
	}
	if h.card != nil {
		return h.card.Engine
	}
	return nil
}

// containers lists the collection paths (root, principal, home) that must be
// addressed with a trailing slash for p's layout.
func (h appleDAV) containers(p string) []string {
	out := []string{h.prefix, h.prefix + "/" + legacyUserSeg, h.prefix + "/" + legacyUserSeg + "/" + h.homeSeg}
	segs, err := Split(strings.TrimPrefix(p, h.prefix))
	if err == nil && len(segs) >= 2 && segs[0] == userSeg {
		base := h.prefix + "/" + userSeg + "/" + segs[1]
		out = append(out, base, base+"/"+h.homeSeg)
	}
	return out
}

func canonicalDAVSlash(w http.ResponseWriter, r *http.Request, need []string) bool {
	p := r.URL.Path
	if strings.HasSuffix(p, "/") {
		return false
	}
	for _, n := range need {
		if p == n {
			http.Redirect(w, r, p+"/", http.StatusPermanentRedirect)
			return true
		}
	}
	return false
}

func servePropPatchOK(w http.ResponseWriter, r *http.Request) {
	// iOS PROPPATCHes calendar-color / calendar-order. go-webdav returns 501,
	// which Calendar.app surfaces as "Calendars could not update".
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	writePropPatchOK(w, r.URL.Path, body)
}

func writePropPatchOK(w http.ResponseWriter, path string, body []byte) {
	href := xmlEscape(path)
	props := propPatchNames(body)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<D:multistatus xmlns:D="DAV:"><D:response><D:href>`)
	b.WriteString(href)
	b.WriteString(`</D:href><D:propstat><D:prop>`)
	for _, p := range props {
		b.WriteString("<")
		b.WriteString(p)
		b.WriteString("/>")
	}
	b.WriteString(`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
	_, _ = w.Write([]byte(b.String()))
}

func propPatchNames(body []byte) []string {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	var names []string
	inProp := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "prop" {
				inProp--
			}
			continue
		}
		if se.Name.Local == "prop" {
			inProp++
			continue
		}
		if inProp > 0 {
			names = append(names, xmlEscape(se.Name.Local))
		}
	}
	return names
}

func (b *CalDAV) createFromPath(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	name := mkcalendarDisplayName(body)
	if name == "" {
		segs, _ := Split(r.URL.Path)
		if len(segs) > 0 {
			name = DAVResourceName(segs[len(segs)-1], "")
		}
	}
	if err := b.CreateCalendar(r.Context(), &caldav.Calendar{Path: r.URL.Path, Name: name}); err != nil {
		if code := httpErrorCode(err); code == http.StatusForbidden || code == http.StatusNotFound {
			http.Error(w, err.Error(), code)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// mkcalendarDisplayName extracts DAV:displayname from a MKCALENDAR body.
func mkcalendarDisplayName(body []byte) string {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	in := false
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			in = t.Name.Local == "displayname"
		case xml.EndElement:
			if t.Name.Local == "displayname" {
				name := strings.TrimSpace(b.String())
				if ValidDisplayName(name) {
					return name
				}
				return ""
			}
		case xml.CharData:
			if in {
				b.Write(t)
			}
		}
	}
	return ""
}

// httpErrorCode returns the status of a go-webdav HTTP error (its type is
// internal to go-webdav, so read the Code field by reflection), or 0.
func httpErrorCode(err error) int {
	for err != nil {
		v := reflect.ValueOf(err)
		if v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().Kind() == reflect.Struct {
			if f := v.Elem().FieldByName("Code"); f.IsValid() && f.Kind() == reflect.Int {
				return int(f.Int())
			}
		}
		err = errors.Unwrap(err)
	}
	return 0
}
