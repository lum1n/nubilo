package agent

import (
	"bytes"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/teambition/rrule-go"
)

// EventSpec is one VEVENT, or a series master plus overrides.
type EventSpec struct {
	UID          string
	Summary      string
	Notes        string
	Location     string
	Start        time.Time
	End          time.Time
	AllDay       bool
	TZ           string // IANA name; empty means UTC
	RRule        string
	ExDates      []time.Time
	RecurrenceID time.Time
	Exceptions   []EventSpec
	URL          string
	Status       string
	Transp       string
	Organizer    PersonSpec
	Attendees    []PersonSpec
	Alarms       []AlarmSpec
}

type PersonSpec struct {
	Name     string
	Email    string
	PartStat string
	Role     string
}

type AlarmSpec struct {
	OffsetSec int64
	Abs       time.Time
	Action    string
	Desc      string
}

func UIDFromICS(ics []byte) string {
	cal, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil || cal == nil {
		return ""
	}
	var fallback string
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent && c.Name != ical.CompToDo {
			continue
		}
		uid, _ := c.Props.Text(ical.PropUID)
		uid = strings.TrimSpace(uid)
		if uid == "" {
			continue
		}
		if c.Props.Get(ical.PropRecurrenceID) == nil {
			return uid
		}
		if fallback == "" {
			fallback = uid
		}
	}
	return fallback
}

func EventStartMS(ics []byte) int64 {
	spec, err := ParseEventICS(ics)
	if err != nil || spec.Start.IsZero() {
		return 0
	}
	return spec.Start.UTC().UnixMilli()
}

func UIDFromVCard(vcf []byte) string {
	card, err := vcard.NewDecoder(bytes.NewReader(vcf)).Decode()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(card.Value(vcard.FieldUID))
}

func EventSummaryStartEnd(ics []byte) (summary string, start, end time.Time) {
	spec, err := ParseEventICS(ics)
	if err != nil {
		start = time.Now().UTC()
		end = start.Add(time.Hour)
		return "", start, end
	}
	end = spec.End
	if end.IsZero() {
		end = spec.Start.Add(time.Hour)
	}
	return spec.Summary, spec.Start, end
}

func EncodeICS(uid, summary string, start, end time.Time) ([]byte, error) {
	return EncodeEventICS(EventSpec{UID: uid, Summary: summary, Start: start, End: end})
}

func EncodeEventICS(spec EventSpec) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//nubilo//agent//EN")
	cal.Children = append(cal.Children, encodeVEVENT(spec, false).Component)
	for _, ex := range spec.Exceptions {
		ex.UID = spec.UID
		if ex.TZ == "" {
			ex.TZ = spec.TZ
		}
		cal.Children = append(cal.Children, encodeVEVENT(ex, true).Component)
	}
	if spec.TZ != "" && locationForTZ(spec.TZ) != time.UTC {
		cal.Props.SetText("X-WR-TIMEZONE", spec.TZ)
		appendVTimezone(cal, spec.TZ)
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeVEVENT(spec EventSpec, isOverride bool) *ical.Event {
	ev := ical.NewEvent()
	if uid := icsUID(spec.UID); uid != "" {
		ev.Props.SetText(ical.PropUID, uid)
	}
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	setEventTime(ev.Props, ical.PropDateTimeStart, spec.Start, spec)
	if !spec.End.IsZero() {
		end := spec.End
		if spec.AllDay {
			end = dateOnly(end)
			start := dateOnly(spec.Start)
			if !end.After(start) {
				end = start.AddDate(0, 0, 1)
			}
		}
		setEventTime(ev.Props, ical.PropDateTimeEnd, end, spec)
	}
	if s := icsText(spec.Summary); s != "" {
		ev.Props.SetText(ical.PropSummary, s)
	}
	if s := icsText(spec.Notes); s != "" {
		ev.Props.SetText(ical.PropDescription, s)
	}
	if s := icsText(spec.Location); s != "" {
		ev.Props.SetText(ical.PropLocation, s)
	}
	if spec.URL != "" {
		setEventURL(ev.Props, spec.URL)
	}
	if s := strings.ToUpper(strings.TrimSpace(spec.Status)); s != "" {
		ev.Props.SetText(ical.PropStatus, s)
	}
	if s := strings.ToUpper(strings.TrimSpace(spec.Transp)); s != "" {
		ev.Props.SetText(ical.PropTransparency, s)
	}
	if addr := calAddress(spec.Organizer.Email); addr != "" {
		ev.Props.Add(personProp(ical.PropOrganizer, spec.Organizer))
	}
	for _, a := range spec.Attendees {
		if calAddress(a.Email) == "" && a.Name == "" {
			continue
		}
		ev.Props.Add(personProp(ical.PropAttendee, a))
	}
	for _, al := range spec.Alarms {
		if c := encodeVALARM(al); c != nil {
			ev.Children = append(ev.Children, c)
		}
	}
	if isOverride && !spec.RecurrenceID.IsZero() {
		setEventTime(ev.Props, ical.PropRecurrenceID, spec.RecurrenceID, spec)
	}
	if !isOverride && spec.RRule != "" {
		if opt, err := rrule.StrToROption(spec.RRule); err == nil && opt != nil {
			ev.Props.SetRecurrenceRule(opt)
		}
	}
	if !isOverride {
		for _, t := range spec.ExDates {
			p := ical.NewProp(ical.PropExceptionDates)
			if spec.AllDay {
				p.SetDate(dateOnly(t))
			} else {
				p.SetDateTime(t.In(locationForTZ(spec.TZ)))
			}
			ev.Props.Add(p)
		}
	}
	return ev
}

func setEventTime(props ical.Props, name string, t time.Time, spec EventSpec) {
	if spec.AllDay {
		props.SetDate(name, dateOnly(t))
		return
	}
	props.SetDateTime(name, t.In(locationForTZ(spec.TZ)))
}

func locationForTZ(tz string) *time.Location {
	tz = strings.TrimSpace(tz)
	if tz == "" || strings.EqualFold(tz, "UTC") || strings.EqualFold(tz, "GMT") {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

func dateOnly(t time.Time) time.Time {
	l := t.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// go-ical SetText escapes LF but leaves CR in the value, and the encoder then
// rejects the property. EventKit notes often contain CR or CRLF.
func icsText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func icsUID(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return strings.TrimSpace(s)
}

func ParseEventICS(ics []byte) (EventSpec, error) {
	var zero EventSpec
	cal, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil || cal == nil {
		return zero, err
	}
	var master EventSpec
	var extras []EventSpec
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		spec := specFromComp(c)
		if c.Props.Get(ical.PropRecurrenceID) != nil {
			extras = append(extras, spec)
			continue
		}
		if master.UID == "" {
			master = spec
		}
	}
	master.Exceptions = extras
	return master, nil
}

func specFromComp(c *ical.Component) EventSpec {
	ev := ical.Event{Component: c}
	spec := EventSpec{}
	spec.UID, _ = c.Props.Text(ical.PropUID)
	spec.Summary, _ = c.Props.Text(ical.PropSummary)
	spec.Notes, _ = c.Props.Text(ical.PropDescription)
	spec.Location, _ = c.Props.Text(ical.PropLocation)
	if p := c.Props.Get(ical.PropDateTimeStart); p != nil {
		if p.ValueType() == ical.ValueDate {
			spec.AllDay = true
		} else {
			spec.TZ = p.Params.Get(ical.PropTimezoneID)
		}
	}
	if t, err := ev.DateTimeStart(time.Local); err == nil {
		spec.Start = t
	}
	if t, err := ev.DateTimeEnd(time.Local); err == nil && !t.IsZero() {
		spec.End = t
	}
	if p := c.Props.Get(ical.PropRecurrenceID); p != nil {
		if t, err := p.DateTime(time.Local); err == nil {
			spec.RecurrenceID = t
		}
	}
	if opt, err := c.Props.RecurrenceRule(); err == nil && opt != nil {
		spec.RRule = opt.RRuleString()
	}
	for _, p := range c.Props.Values(ical.PropExceptionDates) {
		if t, err := p.DateTime(time.Local); err == nil && !t.IsZero() {
			spec.ExDates = append(spec.ExDates, t)
		}
	}
	if p := c.Props.Get(ical.PropURL); p != nil {
		if u, err := p.URI(); err == nil && u != nil {
			spec.URL = u.String()
		} else {
			spec.URL = strings.TrimSpace(p.Value)
		}
	}
	spec.Status, _ = c.Props.Text(ical.PropStatus)
	spec.Transp, _ = c.Props.Text(ical.PropTransparency)
	if p := c.Props.Get(ical.PropOrganizer); p != nil {
		spec.Organizer = parsePerson(p)
	}
	for _, p := range c.Props.Values(ical.PropAttendee) {
		spec.Attendees = append(spec.Attendees, parsePerson(&p))
	}
	for _, child := range c.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		spec.Alarms = append(spec.Alarms, parseVALARM(child))
	}
	return spec
}

func setEventURL(props ical.Props, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		props.SetText(ical.PropURL, icsText(raw))
		return
	}
	props.SetURI(ical.PropURL, u)
}

func personProp(name string, p PersonSpec) *ical.Prop {
	prop := ical.NewProp(name)
	prop.SetValueType(ical.ValueCalendarAddress)
	addr := calAddress(p.Email)
	if addr == "" {
		addr = calAddress(p.Name)
	}
	prop.Value = addr
	if p.Name != "" {
		prop.Params.Set(ical.ParamCommonName, p.Name)
	}
	if p.PartStat != "" {
		prop.Params.Set(ical.ParamParticipationStatus, strings.ToUpper(p.PartStat))
	}
	if p.Role != "" {
		prop.Params.Set(ical.ParamRole, strings.ToUpper(p.Role))
	}
	return prop
}

func parsePerson(p *ical.Prop) PersonSpec {
	if p == nil {
		return PersonSpec{}
	}
	return PersonSpec{
		Name:     p.Params.Get(ical.ParamCommonName),
		Email:    strings.TrimSpace(p.Value),
		PartStat: p.Params.Get(ical.ParamParticipationStatus),
		Role:     p.Params.Get(ical.ParamRole),
	}
}

func encodeVALARM(al AlarmSpec) *ical.Component {
	c := ical.NewComponent(ical.CompAlarm)
	action := al.Action
	if action == "" {
		action = "DISPLAY"
	}
	c.Props.SetText(ical.PropAction, action)
	desc := al.Desc
	if desc == "" {
		desc = "Reminder"
	}
	c.Props.SetText(ical.PropDescription, icsText(desc))
	trig := ical.NewProp(ical.PropTrigger)
	if !al.Abs.IsZero() {
		trig.SetDateTime(al.Abs.UTC())
	} else {
		trig.SetDuration(time.Duration(al.OffsetSec) * time.Second)
	}
	c.Props.Set(trig)
	return c
}

func parseVALARM(c *ical.Component) AlarmSpec {
	al := AlarmSpec{Action: "DISPLAY"}
	if s, err := c.Props.Text(ical.PropAction); err == nil && s != "" {
		al.Action = s
	}
	al.Desc, _ = c.Props.Text(ical.PropDescription)
	p := c.Props.Get(ical.PropTrigger)
	if p == nil {
		return al
	}
	if d, err := p.Duration(); err == nil && d != 0 {
		al.OffsetSec = int64(d / time.Second)
		return al
	}
	if t, err := p.DateTime(time.UTC); err == nil && !t.IsZero() {
		al.Abs = t
	}
	return al
}

func expandRRule(rule string, start, from, to time.Time) []time.Time {
	if rule == "" {
		return nil
	}
	opt, err := rrule.StrToROption(rule)
	if err != nil || opt == nil {
		return nil
	}
	opt.Dtstart = start
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil
	}
	return r.Between(from.Add(-time.Second), to.Add(time.Second), true)
}
