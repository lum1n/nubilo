package agent

import (
	"bytes"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
)

func UIDFromICS(ics []byte) string {
	cal, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil || cal == nil {
		return ""
	}
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent || c.Name == ical.CompToDo {
			uid, _ := c.Props.Text(ical.PropUID)
			return strings.TrimSpace(uid)
		}
	}
	return ""
}

func EventStartMS(ics []byte) int64 {
	cal, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil || cal == nil {
		return 0
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		ev := ical.Event{Component: c}
		t, err := ev.DateTimeStart(time.UTC)
		if err != nil {
			return 0
		}
		return t.UTC().UnixMilli()
	}
	return 0
}

func UIDFromVCard(vcf []byte) string {
	card, err := vcard.NewDecoder(bytes.NewReader(vcf)).Decode()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(card.Value(vcard.FieldUID))
}

func EventSummaryStartEnd(ics []byte) (summary string, start, end time.Time) {
	start = time.Now().UTC()
	end = start.Add(time.Hour)
	cal, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil || cal == nil {
		return "", start, end
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		summary, _ = c.Props.Text(ical.PropSummary)
		ev := ical.Event{Component: c}
		if t, err := ev.DateTimeStart(time.UTC); err == nil {
			start = t.UTC()
		}
		if t, err := ev.DateTimeEnd(time.UTC); err == nil && !t.IsZero() {
			end = t.UTC()
		}
		return summary, start, end
	}
	return summary, start, end
}

func EncodeICS(uid, summary string, start, end time.Time) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//nubilo//agent//EN")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	ev.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
	if !end.IsZero() {
		ev.Props.SetDateTime(ical.PropDateTimeEnd, end.UTC())
	}
	if summary != "" {
		ev.Props.SetText(ical.PropSummary, summary)
	}
	cal.Children = append(cal.Children, ev.Component)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
