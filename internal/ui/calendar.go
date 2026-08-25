package ui

import (
	"bytes"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

type eventPreview struct {
	Summary string `json:"summary"`
	Start   int64  `json:"start_ms,omitempty"`
	End     int64  `json:"end_ms,omitempty"`
	Due     int64  `json:"due_ms,omitempty"`
	AllDay  bool   `json:"all_day,omitempty"`
	UID     string `json:"uid,omitempty"`
	Status  string `json:"status,omitempty"`
	Comp    string `json:"comp,omitempty"`
}

func previewICS(data []byte) eventPreview {
	var out eventPreview
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil || cal == nil {
		return out
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent && c.Name != ical.CompToDo {
			continue
		}
		out.Comp = c.Name
		out.Summary, _ = c.Props.Text(ical.PropSummary)
		out.UID, _ = c.Props.Text(ical.PropUID)
		out.Status, _ = c.Props.Text(ical.PropStatus)
		if p := c.Props.Get(ical.PropDateTimeStart); p != nil {
			out.AllDay = p.ValueType() == ical.ValueDate
			if t, err := p.DateTime(time.Local); err == nil {
				out.Start = t.UnixMilli()
			}
		}
		if p := c.Props.Get(ical.PropDateTimeEnd); p != nil {
			if t, err := p.DateTime(time.Local); err == nil {
				out.End = t.UnixMilli()
			}
		}
		if p := c.Props.Get(ical.PropDue); p != nil {
			if t, err := p.DateTime(time.Local); err == nil {
				out.Due = t.UnixMilli()
				if out.Start == 0 {
					out.Start = t.UnixMilli()
				}
			}
		}
		if out.Summary == "" {
			out.Summary = strings.ToUpper(c.Name)
		}
		break
	}
	return out
}
