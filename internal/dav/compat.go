package dav

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"

	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// WellKnown serves RFC 6764 discovery. iOS Calendar sends PROPFIND (not GET)
// to /.well-known/caldav. GET/HEAD stay unauthenticated redirects; other
// methods are rewritten to the DAV principal so Basic auth and PROPFIND work.
func WellKnown(principal string, dav http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	inner := &caldav.Handler{Backend: b, Prefix: b.Prefix}
	return appleDAV{
		next: inner,
		slash: []string{
			b.Prefix,
			b.Prefix + "/" + calUserSeg,
			b.Prefix + "/" + calUserSeg + "/" + calHomeSeg,
		},
		mkcalendar: b,
	}
}

// WrapCardDAV adds Apple Contacts compatibility around go-webdav.
func WrapCardDAV(b *CardDAV) http.Handler {
	inner := &carddav.Handler{Backend: b, Prefix: b.Prefix}
	return appleDAV{
		next: inner,
		slash: []string{
			b.Prefix,
			b.Prefix + "/" + cardUserSeg,
			b.Prefix + "/" + cardUserSeg + "/" + cardHomeSeg,
		},
	}
}

type mkCalendarHandler interface {
	createFromPath(w http.ResponseWriter, r *http.Request)
}

type appleDAV struct {
	next       http.Handler
	slash      []string
	mkcalendar mkCalendarHandler
}

func (h appleDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if canonicalDAVSlash(w, r, h.slash) {
		return
	}
	switch r.Method {
	case "PROPPATCH":
		servePropPatchOK(w, r)
		return
	case "MKCALENDAR":
		if h.mkcalendar != nil {
			h.mkcalendar.createFromPath(w, r)
			return
		}
	}
	h.next.ServeHTTP(w, r)
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
	href := xmlEscape(r.URL.Path)
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
	_, name, file, err := b.parseCalPath(r.URL.Path)
	if err != nil || file != "" || name == "" {
		http.Error(w, "calendar creation not allowed here", http.StatusForbidden)
		return
	}
	name = DAVResourceName(name, "")
	if err := b.CreateCalendar(r.Context(), &caldav.Calendar{Path: r.URL.Path, Name: name}); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
