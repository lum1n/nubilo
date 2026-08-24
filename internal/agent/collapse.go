package agent

import (
	"fmt"
	"strings"
	"time"
)

// ekOccurrence is one EventKit row (expanded occurrence or detached instance).
type ekOccurrence struct {
	ID          string  `json:"id"`
	EventID     string  `json:"event_id"`
	CalendarID  string  `json:"calendar_id"`
	UID         string  `json:"uid"`
	Title       string  `json:"title"`
	Notes       string  `json:"notes"`
	Location    string  `json:"location"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	AllDay      int     `json:"all_day"`
	Detached    int     `json:"detached"`
	Occurrence  float64 `json:"occurrence"`
	MasterStart float64 `json:"master_start"`
	MasterEnd   float64 `json:"master_end"`
	RRule       string  `json:"rrule"`
}

type seriesAcc struct {
	master     ekOccurrence
	occs       []time.Time
	exceptions []ekOccurrence
}

// collapseEKEvents turns EventKit's expanded occurrences into one LocalEvent
// per UID: a VEVENT with RRULE, EXDATE for deletions in-window, and
// RECURRENCE-ID overrides for detached instances.
func collapseEKEvents(rows []ekOccurrence, winStart, winEnd time.Time) ([]LocalEvent, error) {
	series := map[string]*seriesAcc{}
	var singles []ekOccurrence
	for _, r := range rows {
		if r.Detached != 0 {
			sid := parentEventID(r.EventID)
			if sid == "" {
				singles = append(singles, r)
				continue
			}
			acc := series[sid]
			if acc == nil {
				acc = &seriesAcc{}
				series[sid] = acc
			}
			acc.exceptions = append(acc.exceptions, r)
			continue
		}
		if strings.TrimSpace(r.RRule) != "" {
			sid := r.EventID
			if sid == "" {
				sid = r.ID
			}
			acc := series[sid]
			if acc == nil {
				acc = &seriesAcc{master: r}
				series[sid] = acc
			} else if acc.master.ID == "" {
				acc.master = r
			}
			acc.occs = append(acc.occs, occTime(r))
			continue
		}
		singles = append(singles, r)
	}

	out := make([]LocalEvent, 0, len(singles)+len(series))
	for _, r := range singles {
		ev, err := localFromOccurrence(r)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	for _, acc := range series {
		if acc.master.ID == "" {
			for _, r := range acc.exceptions {
				ev, err := localFromOccurrence(r)
				if err != nil {
					return nil, err
				}
				out = append(out, ev)
			}
			continue
		}
		ev, err := localFromSeries(acc, winStart, winEnd)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func localFromOccurrence(r ekOccurrence) (LocalEvent, error) {
	spec := specFromOccurrence(r)
	ics, err := EncodeEventICS(spec)
	if err != nil {
		return LocalEvent{}, err
	}
	id := r.ID
	if id == "" {
		id = r.EventID
	}
	return LocalEvent{ID: id, CalendarID: r.CalendarID, UID: spec.UID, ICS: ics, StartMS: spec.Start.UTC().UnixMilli()}, nil
}

func localFromSeries(acc *seriesAcc, winStart, winEnd time.Time) (LocalEvent, error) {
	m := acc.master
	spec := specFromOccurrence(m)
	if m.MasterStart != 0 {
		spec.Start = time.Unix(int64(m.MasterStart), 0)
		if m.AllDay != 0 {
			spec.Start = dateOnly(spec.Start)
		} else {
			spec.Start = spec.Start.UTC()
		}
	}
	if m.MasterEnd != 0 {
		spec.End = time.Unix(int64(m.MasterEnd), 0)
		if m.AllDay != 0 {
			spec.End = dateOnly(spec.End)
		} else {
			spec.End = spec.End.UTC()
		}
	}
	spec.RRule = strings.TrimSpace(m.RRule)
	seen := map[string]bool{}
	for _, t := range acc.occs {
		seen[occKey(t, spec.AllDay)] = true
	}
	exRID := map[string]bool{}
	for _, ex := range acc.exceptions {
		es := specFromOccurrence(ex)
		es.UID = spec.UID
		es.RecurrenceID = occTime(ex)
		if es.RecurrenceID.IsZero() {
			es.RecurrenceID = es.Start
		}
		spec.Exceptions = append(spec.Exceptions, es)
		exRID[occKey(es.RecurrenceID, spec.AllDay)] = true
	}
	for _, t := range expandRRule(spec.RRule, spec.Start, winStart, winEnd) {
		k := occKey(t, spec.AllDay)
		if seen[k] || exRID[k] {
			continue
		}
		spec.ExDates = append(spec.ExDates, t)
	}
	ics, err := EncodeEventICS(spec)
	if err != nil {
		return LocalEvent{}, err
	}
	id := m.ID
	if id == "" {
		id = m.EventID
	}
	return LocalEvent{ID: id, CalendarID: m.CalendarID, UID: spec.UID, ICS: ics, StartMS: spec.Start.UTC().UnixMilli()}, nil
}

func specFromOccurrence(r ekOccurrence) EventSpec {
	st := time.Unix(int64(r.Start), 0)
	en := time.Unix(int64(r.End), 0)
	allDay := r.AllDay != 0
	if allDay {
		st = dateOnly(st)
		en = dateOnly(en)
	} else {
		st = st.UTC()
		en = en.UTC()
	}
	uid := r.UID
	if uid == "" {
		uid = r.ID
	}
	return EventSpec{
		UID: uid, Summary: r.Title, Notes: r.Notes, Location: r.Location,
		Start: st, End: en, AllDay: allDay,
	}
}

func occTime(r ekOccurrence) time.Time {
	sec := r.Occurrence
	if sec == 0 {
		sec = r.Start
	}
	t := time.Unix(int64(sec), 0)
	if r.AllDay != 0 {
		return dateOnly(t)
	}
	return t.UTC()
}

func occKey(t time.Time, allDay bool) string {
	if allDay {
		d := dateOnly(t)
		return fmt.Sprintf("%04d-%02d-%02d", d.Year(), d.Month(), d.Day())
	}
	return t.UTC().Truncate(time.Minute).Format(time.RFC3339)
}

func parentEventID(eventID string) string {
	i := strings.Index(eventID, "/")
	if i <= 0 {
		return ""
	}
	return eventID[:i]
}
