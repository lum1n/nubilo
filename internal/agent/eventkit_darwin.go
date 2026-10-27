//go:build darwin && cgo

package agent

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework EventKit -framework Foundation -framework CoreGraphics
#include "eventkit_darwin.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"errors"
	"time"
	"unsafe"
)

// ekSource reads Calendar through EventKit. The first call asks for
// Calendar access (System Settings → Privacy & Security → Calendars).
type ekSource struct{}

// OpenCalendars returns the Mac's calendars for mirroring.
func OpenCalendars() (CalendarSource, error) { return &ekSource{}, nil }

// PlatformCalendars lists the Mac's calendars.
func PlatformCalendars() ([]CalendarInfo, error) { return (&ekSource{}).ListCalendars() }

func cErr(err **C.char) error {
	if err == nil || *err == nil {
		return errors.New("eventkit error")
	}
	defer C.free(unsafe.Pointer(*err))
	return errors.New(C.GoString(*err))
}

func cStr(p *C.char) string {
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

func (e *ekSource) ListCalendars() ([]CalendarInfo, error) {
	var err *C.char
	p := C.nubilo_ek_list_calendars(&err)
	if p == nil {
		return nil, cErr(&err)
	}
	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal([]byte(cStr(p)), &rows); err != nil {
		return nil, err
	}
	out := make([]CalendarInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, CalendarInfo{ID: r.ID, Title: r.Title, Color: r.Color})
	}
	return out, nil
}

func (e *ekSource) ListEvents(calendarID string, start, end time.Time) ([]LocalEvent, error) {
	cid := C.CString(calendarID)
	defer C.free(unsafe.Pointer(cid))
	var err *C.char
	p := C.nubilo_ek_list_events(cid, C.double(start.Unix()), C.double(end.Unix()), &err)
	if p == nil {
		return nil, cErr(&err)
	}
	var rows []ekOccurrence
	if err := json.Unmarshal([]byte(cStr(p)), &rows); err != nil {
		return nil, err
	}
	return collapseEKEvents(rows, start, end)
}
