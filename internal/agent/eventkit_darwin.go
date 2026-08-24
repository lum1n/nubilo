//go:build darwin

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
		Color string `json:"color"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
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

func (e *ekSource) ListReminderLists() ([]CalendarInfo, error) {
	var err *C.char
	p := C.nubilo_ek_list_reminder_lists(&err)
	if p == nil {
		return nil, cErr(&err)
	}
	raw := cStr(p)
	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	out := make([]CalendarInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, CalendarInfo{ID: r.ID, Title: r.Title, Color: r.Color})
	}
	return out, nil
}

type ekReminderRow struct {
	ID        string    `json:"id"`
	ListID    string    `json:"list_id"`
	UID       string    `json:"uid"`
	Title     string    `json:"title"`
	Notes     string    `json:"notes"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	Priority  int       `json:"priority"`
	Percent   int       `json:"percent"`
	AllDay    int       `json:"all_day"`
	Start     float64   `json:"start"`
	Due       float64   `json:"due"`
	Completed float64   `json:"completed"`
	RRule     string    `json:"rrule"`
	Alarms    []ekAlarm `json:"alarms"`
}

func (e *ekSource) ListReminders(listID string, start, end time.Time) ([]LocalTodo, error) {
	cid := C.CString(listID)
	defer C.free(unsafe.Pointer(cid))
	var err *C.char
	p := C.nubilo_ek_list_reminders(cid, C.double(start.Unix()), C.double(end.Unix()), &err)
	if p == nil {
		return nil, cErr(&err)
	}
	raw := cStr(p)
	var rows []ekReminderRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	out := make([]LocalTodo, 0, len(rows))
	for _, r := range rows {
		spec := reminderRowToSpec(r)
		if !todoInWindow(spec, start, end) {
			continue
		}
		ics, err := EncodeTodoICS(spec)
		if err != nil {
			return nil, err
		}
		dueMS := TodoDueMS(ics)
		out = append(out, LocalTodo{
			ID: r.ID, ListID: r.ListID, UID: spec.UID, ICS: ics, DueMS: dueMS,
		})
	}
	return out, nil
}

func reminderRowToSpec(r ekReminderRow) TodoSpec {
	spec := TodoSpec{
		UID: r.UID, Summary: r.Title, Notes: r.Notes, URL: r.URL,
		Status: r.Status, Priority: r.Priority, RRule: r.RRule, Percent: -1,
	}
	if r.UID == "" {
		spec.UID = r.ID
	}
	if r.AllDay != 0 {
		spec.AllDay = true
	}
	if r.Percent > 0 {
		spec.Percent = r.Percent
	}
	if r.Start > 0 {
		spec.Start = time.Unix(int64(r.Start), 0)
	}
	if r.Due > 0 {
		spec.Due = time.Unix(int64(r.Due), 0)
	}
	if r.Completed > 0 {
		spec.Completed = time.Unix(int64(r.Completed), 0)
	}
	for _, a := range r.Alarms {
		al := AlarmSpec{Action: a.Action, Desc: a.Desc}
		if a.Abs != nil {
			al.Abs = time.Unix(int64(*a.Abs), 0)
		} else if a.Offset != nil {
			al.OffsetSec = int64(*a.Offset)
		}
		spec.Alarms = append(spec.Alarms, al)
	}
	return spec
}

func (e *ekSource) UpsertReminder(listID, localID string, ics []byte) (string, error) {
	spec, err := ParseTodoICS(ics)
	if err != nil {
		return "", err
	}
	payload, err := todoToSaveJSON(spec)
	if err != nil {
		return "", err
	}
	cid := C.CString(listID)
	defer C.free(unsafe.Pointer(cid))
	iid := C.CString(localID)
	defer C.free(unsafe.Pointer(iid))
	cjson := C.CString(string(payload))
	defer C.free(unsafe.Pointer(cjson))
	var cerr *C.char
	p := C.nubilo_ek_save_reminder(cid, iid, cjson, &cerr)
	if p == nil {
		return "", cErr(&cerr)
	}
	return cStr(p), nil
}

func (e *ekSource) DeleteReminder(localID string) error {
	iid := C.CString(localID)
	defer C.free(unsafe.Pointer(iid))
	var err *C.char
	if C.nubilo_ek_delete_reminder(iid, &err) == 0 {
		return cErr(&err)
	}
	return nil
}
