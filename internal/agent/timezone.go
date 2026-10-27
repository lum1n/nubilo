package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

func appendVTimezone(cal *ical.Calendar, tz string) {
	loc := locationForTZ(tz)
	if loc == time.UTC {
		return
	}
	now := time.Now().In(loc)
	from := time.Date(now.Year()-5, 1, 1, 0, 0, 0, 0, loc)
	to := time.Date(now.Year()+10, 1, 1, 0, 0, 0, 0, loc)
	steps := timezoneSteps(loc, from, to)
	if len(steps) == 0 {
		return
	}
	tzc := ical.NewComponent(ical.CompTimezone)
	tzc.Props.SetText(ical.PropTimezoneID, loc.String())
	maxOff := steps[0].off
	for _, s := range steps {
		if s.off > maxOff {
			maxOff = s.off
		}
	}
	for i, s := range steps {
		fromOff := s.off
		if i > 0 {
			fromOff = steps[i-1].off
		}
		name := ical.CompTimezoneStandard
		if s.off >= maxOff && maxOff != fromOff {
			name = ical.CompTimezoneDaylight
		}
		child := ical.NewComponent(name)
		start := ical.NewProp(ical.PropDateTimeStart)
		start.SetValueType(ical.ValueDateTime)
		start.Value = s.at.In(loc).Format("20060102T150405")
		child.Props.Set(start)
		child.Props.SetText(ical.PropTimezoneOffsetFrom, tzOffset(fromOff))
		child.Props.SetText(ical.PropTimezoneOffsetTo, tzOffset(s.off))
		if s.name != "" {
			child.Props.SetText(ical.PropTimezoneName, s.name)
		}
		tzc.Children = append(tzc.Children, child)
	}
	cal.Children = append([]*ical.Component{tzc}, cal.Children...)
}

type tzStep struct {
	at   time.Time
	name string
	off  int
}

func timezoneSteps(loc *time.Location, from, to time.Time) []tzStep {
	t := from.In(loc)
	name, off := t.Zone()
	out := []tzStep{{at: t, name: name, off: off}}
	for t = t.Add(12 * time.Hour); t.Before(to); t = t.Add(12 * time.Hour) {
		n, o := t.In(loc).Zone()
		if n == name && o == off {
			continue
		}
		hit := refineTZChange(loc, t.Add(-12*time.Hour), t, name, off)
		name, off = n, o
		out = append(out, tzStep{at: hit, name: n, off: o})
	}
	return out
}

func refineTZChange(loc *time.Location, lo, hi time.Time, prevName string, prevOff int) time.Time {
	for hi.Sub(lo) > time.Minute {
		mid := lo.Add(hi.Sub(lo) / 2)
		n, o := mid.In(loc).Zone()
		if n == prevName && o == prevOff {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi.In(loc)
}

func tzOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	return fmt.Sprintf("%s%02d%02d", sign, sec/3600, (sec%3600)/60)
}

func calAddress(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, ":") {
		return s
	}
	return "mailto:" + s
}
