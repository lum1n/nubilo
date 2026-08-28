package agent

import (
	"encoding/json"
	"fmt"

	"github.com/teambition/rrule-go"
)

type eventSaveJSON struct {
	UID        string          `json:"uid"`
	Title      string          `json:"title"`
	Notes      string          `json:"notes"`
	Location   string          `json:"location"`
	Start      float64         `json:"start"`
	End        float64         `json:"end"`
	AllDay     int             `json:"all_day"`
	URL        string          `json:"url,omitempty"`
	Status     string          `json:"status,omitempty"`
	Transp     string          `json:"transp,omitempty"`
	Organizer  *PersonSpec     `json:"organizer,omitempty"`
	Attendees  []PersonSpec    `json:"attendees,omitempty"`
	Alarms     []ekAlarm       `json:"alarms,omitempty"`
	RRule      *recurrenceJSON `json:"rrule,omitempty"`
	ExDates    []float64       `json:"exdates,omitempty"`
	Exceptions []exceptionJSON `json:"exceptions,omitempty"`
}

type recurrenceJSON struct {
	Freq       string   `json:"freq"`
	Interval   int      `json:"interval"`
	Count      int      `json:"count,omitempty"`
	Until      float64  `json:"until,omitempty"`
	ByDay      []string `json:"byday,omitempty"`
	ByMonthDay []int    `json:"bymonthday,omitempty"`
	ByMonth    []int    `json:"bymonth,omitempty"`
	BySetPos   []int    `json:"bysetpos,omitempty"`
	ByYearDay  []int    `json:"byyearday,omitempty"`
	ByWeekNo   []int    `json:"byweekno,omitempty"`
	Wkst       string   `json:"wkst,omitempty"`
}

type exceptionJSON struct {
	RID      float64 `json:"rid"`
	Title    string  `json:"title"`
	Notes    string  `json:"notes"`
	Location string  `json:"location"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	AllDay   int     `json:"all_day"`
}

func specToSaveJSON(spec EventSpec) ([]byte, error) {
	j := eventSaveJSON{
		UID: spec.UID, Title: spec.Summary, Notes: spec.Notes, Location: spec.Location,
		Start: float64(spec.Start.Unix()), End: float64(spec.End.Unix()),
		URL: spec.URL, Status: spec.Status, Transp: spec.Transp, Attendees: spec.Attendees,
	}
	if spec.Organizer.Email != "" || spec.Organizer.Name != "" {
		org := spec.Organizer
		j.Organizer = &org
	}
	for _, al := range spec.Alarms {
		ea := ekAlarm{Action: al.Action, Desc: al.Desc, Email: al.Email}
		if !al.Abs.IsZero() {
			v := float64(al.Abs.Unix())
			ea.Abs = &v
		} else {
			v := float64(al.OffsetSec)
			ea.Offset = &v
		}
		j.Alarms = append(j.Alarms, ea)
	}
	if spec.AllDay {
		j.AllDay = 1
	}
	if spec.RRule != "" {
		j.RRule = rruleToJSON(spec.RRule)
	}
	for _, t := range spec.ExDates {
		j.ExDates = append(j.ExDates, float64(t.Unix()))
	}
	for _, ex := range spec.Exceptions {
		all := 0
		if ex.AllDay {
			all = 1
		}
		rid := ex.RecurrenceID
		if rid.IsZero() {
			rid = ex.Start
		}
		j.Exceptions = append(j.Exceptions, exceptionJSON{
			RID: float64(rid.Unix()), Title: ex.Summary, Notes: ex.Notes, Location: ex.Location,
			Start: float64(ex.Start.Unix()), End: float64(ex.End.Unix()), AllDay: all,
		})
	}
	return json.Marshal(j)
}

type todoSaveJSON struct {
	UID       string          `json:"uid"`
	Title     string          `json:"title"`
	Notes     string          `json:"notes"`
	Start     float64         `json:"start,omitempty"`
	Due       float64         `json:"due,omitempty"`
	Completed float64         `json:"completed,omitempty"`
	AllDay    int             `json:"all_day"`
	URL       string          `json:"url,omitempty"`
	Status    string          `json:"status,omitempty"`
	Priority  int             `json:"priority,omitempty"`
	Alarms    []ekAlarm       `json:"alarms,omitempty"`
	RRule     *recurrenceJSON `json:"rrule,omitempty"`
}

func todoToSaveJSON(spec TodoSpec) ([]byte, error) {
	j := todoSaveJSON{
		UID: spec.UID, Title: spec.Summary, Notes: spec.Notes,
		URL: spec.URL, Status: spec.Status, Priority: spec.Priority,
	}
	if !spec.Start.IsZero() {
		j.Start = float64(spec.Start.Unix())
	}
	if !spec.Due.IsZero() {
		j.Due = float64(spec.Due.Unix())
	}
	if !spec.Completed.IsZero() {
		j.Completed = float64(spec.Completed.Unix())
	}
	if spec.AllDay {
		j.AllDay = 1
	}
	for _, al := range spec.Alarms {
		ea := ekAlarm{Action: al.Action, Desc: al.Desc, Email: al.Email}
		if !al.Abs.IsZero() {
			v := float64(al.Abs.Unix())
			ea.Abs = &v
		} else {
			v := float64(al.OffsetSec)
			ea.Offset = &v
		}
		j.Alarms = append(j.Alarms, ea)
	}
	if spec.RRule != "" {
		j.RRule = rruleToJSON(spec.RRule)
	}
	return json.Marshal(j)
}

func rruleToJSON(s string) *recurrenceJSON {
	opt, err := rrule.StrToROption(s)
	if err != nil || opt == nil {
		return nil
	}
	out := &recurrenceJSON{Interval: opt.Interval, Count: opt.Count}
	if out.Interval <= 0 {
		out.Interval = 1
	}
	switch opt.Freq {
	case rrule.DAILY:
		out.Freq = "DAILY"
	case rrule.WEEKLY:
		out.Freq = "WEEKLY"
	case rrule.MONTHLY:
		out.Freq = "MONTHLY"
	case rrule.YEARLY:
		out.Freq = "YEARLY"
	default:
		return nil
	}
	if !opt.Until.IsZero() {
		out.Until = float64(opt.Until.Unix())
	}
	names := []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}
	for _, w := range opt.Byweekday {
		if w.Day() < 0 || w.Day() > 6 {
			continue
		}
		n := names[w.Day()]
		if w.N() != 0 {
			n = fmt.Sprintf("%d%s", w.N(), n)
		}
		out.ByDay = append(out.ByDay, n)
	}
	out.ByMonthDay = append([]int(nil), opt.Bymonthday...)
	out.ByMonth = append([]int(nil), opt.Bymonth...)
	out.BySetPos = append([]int(nil), opt.Bysetpos...)
	out.ByYearDay = append([]int(nil), opt.Byyearday...)
	out.ByWeekNo = append([]int(nil), opt.Byweekno...)
	if opt.Wkst.Day() != 0 || opt.Wkst.N() != 0 {
		// default WKST is Monday (day 0); only emit when EventKit/RFC set something else
	}
	return out
}
