package agent

import (
	"bytes"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// TodoSpec is one VTODO (Reminders.app item).
type TodoSpec struct {
	UID       string
	Summary   string
	Notes     string
	Start     time.Time
	Due       time.Time
	Completed time.Time
	AllDay    bool
	TZ        string
	Status    string
	Percent   int // 0-100; omitted when negative
	Priority  int // 0-9; 0 means unset
	URL       string
	RRule     string
	Alarms    []AlarmSpec
}

func EncodeTodoICS(spec TodoSpec) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//nubilo//agent//EN")
	cal.Children = append(cal.Children, encodeVTODO(spec))
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

func encodeVTODO(spec TodoSpec) *ical.Component {
	todo := ical.NewComponent(ical.CompToDo)
	if uid := icsUID(spec.UID); uid != "" {
		todo.Props.SetText(ical.PropUID, uid)
	}
	todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	if !spec.Start.IsZero() {
		setTodoTime(todo.Props, ical.PropDateTimeStart, spec.Start, spec)
	}
	if !spec.Due.IsZero() {
		setTodoTime(todo.Props, ical.PropDue, spec.Due, spec)
	}
	if s := icsText(spec.Summary); s != "" {
		todo.Props.SetText(ical.PropSummary, s)
	}
	if s := icsText(spec.Notes); s != "" {
		todo.Props.SetText(ical.PropDescription, s)
	}
	status := strings.ToUpper(strings.TrimSpace(spec.Status))
	if !spec.Completed.IsZero() {
		todo.Props.SetDateTime(ical.PropCompleted, spec.Completed.UTC())
		if status == "" {
			status = "COMPLETED"
		}
		if spec.Percent < 0 {
			spec.Percent = 100
		}
	}
	if status != "" {
		todo.Props.SetText(ical.PropStatus, status)
	}
	if spec.Percent > 0 {
		setICSInt(todo.Props, ical.PropPercentComplete, spec.Percent)
	}
	if spec.Priority > 0 {
		setICSInt(todo.Props, ical.PropPriority, spec.Priority)
	}
	if spec.URL != "" {
		setEventURL(todo.Props, spec.URL)
	}
	if spec.RRule != "" {
		if opt, err := rrule.StrToROption(spec.RRule); err == nil && opt != nil {
			todo.Props.SetRecurrenceRule(opt)
		}
	}
	for _, al := range spec.Alarms {
		if c := encodeVALARM(al); c != nil {
			todo.Children = append(todo.Children, c)
		}
	}
	return todo
}

func setTodoTime(props ical.Props, name string, t time.Time, spec TodoSpec) {
	if spec.AllDay {
		props.SetDate(name, dateOnly(t))
		return
	}
	props.SetDateTime(name, t.In(locationForTZ(spec.TZ)))
}

func setICSInt(props ical.Props, name string, n int) {
	p := ical.NewProp(name)
	p.SetValueType(ical.ValueInt)
	p.Value = strconv.Itoa(n)
	props.Set(p)
}

func ParseTodoICS(ics []byte) (TodoSpec, error) {
	var zero TodoSpec
	zero.Percent = -1
	cal, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil || cal == nil {
		return zero, err
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompToDo {
			continue
		}
		return specFromTodo(c), nil
	}
	return zero, nil
}

func specFromTodo(c *ical.Component) TodoSpec {
	spec := TodoSpec{Percent: -1}
	spec.UID, _ = c.Props.Text(ical.PropUID)
	spec.Summary, _ = c.Props.Text(ical.PropSummary)
	spec.Notes, _ = c.Props.Text(ical.PropDescription)
	spec.Status, _ = c.Props.Text(ical.PropStatus)
	if p := c.Props.Get(ical.PropURL); p != nil {
		if u, err := p.URI(); err == nil && u != nil {
			spec.URL = u.String()
		} else {
			spec.URL = strings.TrimSpace(p.Value)
		}
	}
	if p := c.Props.Get(ical.PropDateTimeStart); p != nil {
		if p.ValueType() == ical.ValueDate {
			spec.AllDay = true
		} else {
			spec.TZ = p.Params.Get(ical.PropTimezoneID)
		}
		if t, err := p.DateTime(time.Local); err == nil {
			spec.Start = t
		}
	}
	if p := c.Props.Get(ical.PropDue); p != nil {
		if p.ValueType() == ical.ValueDate {
			spec.AllDay = true
		} else if spec.TZ == "" {
			spec.TZ = p.Params.Get(ical.PropTimezoneID)
		}
		if t, err := p.DateTime(time.Local); err == nil {
			spec.Due = t
		}
	}
	if p := c.Props.Get(ical.PropCompleted); p != nil {
		if t, err := p.DateTime(time.UTC); err == nil {
			spec.Completed = t
		}
	}
	if p := c.Props.Get(ical.PropPercentComplete); p != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(p.Value)); err == nil {
			spec.Percent = n
		}
	}
	if p := c.Props.Get(ical.PropPriority); p != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(p.Value)); err == nil {
			spec.Priority = n
		}
	}
	if opt, err := c.Props.RecurrenceRule(); err == nil && opt != nil {
		spec.RRule = opt.RRuleString()
	}
	for _, child := range c.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		spec.Alarms = append(spec.Alarms, parseVALARM(child))
	}
	return spec
}

func TodoDueMS(ics []byte) int64 {
	spec, err := ParseTodoICS(ics)
	if err != nil {
		return 0
	}
	if !spec.Completed.IsZero() {
		return spec.Completed.UTC().UnixMilli()
	}
	if !spec.Due.IsZero() {
		return spec.Due.UTC().UnixMilli()
	}
	return 0
}

func todoInWindow(spec TodoSpec, start, end time.Time) bool {
	completed := !spec.Completed.IsZero() || strings.EqualFold(spec.Status, "COMPLETED")
	if !completed {
		return true
	}
	ref := spec.Completed
	if ref.IsZero() {
		ref = spec.Due
	}
	if ref.IsZero() {
		return true
	}
	t := ref.UTC()
	return !t.Before(start.UTC()) && !t.After(end.UTC())
}
