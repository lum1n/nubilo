//go:build darwin

package agent

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework EventKit -framework Foundation
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

type ekSource struct{}

func openEventKit() (*ekSource, error) {
	return &ekSource{}, nil
}

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
	raw := cStr(p)
	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	out := make([]CalendarInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, CalendarInfo{ID: r.ID, Title: r.Title})
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
	raw := cStr(p)
	var rows []ekOccurrence
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	return collapseEKEvents(rows, start, end)
}

func (e *ekSource) UpsertEvent(calendarID, localID string, ics []byte) (string, error) {
	spec, err := ParseEventICS(ics)
	if err != nil {
		return "", err
	}
	payload, err := specToSaveJSON(spec)
	if err != nil {
		return "", err
	}
	cid := C.CString(calendarID)
	defer C.free(unsafe.Pointer(cid))
	iid := C.CString(localID)
	defer C.free(unsafe.Pointer(iid))
	cjson := C.CString(string(payload))
	defer C.free(unsafe.Pointer(cjson))
	var cerr *C.char
	p := C.nubilo_ek_save_event(cid, iid, cjson, &cerr)
	if p == nil {
		return "", cErr(&cerr)
	}
	return cStr(p), nil
}

func (e *ekSource) DeleteEvent(localID string) error {
	iid := C.CString(localID)
	defer C.free(unsafe.Pointer(iid))
	var err *C.char
	if C.nubilo_ek_delete_event(iid, &err) == 0 {
		return cErr(&err)
	}
	return nil
}
