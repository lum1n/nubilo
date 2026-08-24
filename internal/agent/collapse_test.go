package agent

import (
	"strings"
	"testing"
	"time"
)

func TestCollapseWeeklySeries(t *testing.T) {
	start := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC) // Monday
	var rows []ekOccurrence
	for i := 0; i < 4; i++ {
		occ := start.AddDate(0, 0, 7*i)
		rows = append(rows, ekOccurrence{
			ID: "series-1", EventID: "series-1", CalendarID: "cal", UID: "uid-week",
			Title: "Standup", Start: float64(occ.Unix()), End: float64(occ.Add(time.Hour).Unix()),
			Occurrence: float64(occ.Unix()), MasterStart: float64(start.Unix()),
			MasterEnd: float64(start.Add(time.Hour).Unix()),
			RRule:     "FREQ=WEEKLY;BYDAY=MO",
		})
	}
	evs, err := collapseEKEvents(rows, start.Add(-time.Hour), start.AddDate(0, 0, 28))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 series, got %d", len(evs))
	}
	ics := string(evs[0].ICS)
	if !strings.Contains(ics, "RRULE") || !strings.Contains(ics, "FREQ=WEEKLY") {
		t.Fatalf("missing rrule: %s", ics)
	}
	if strings.Count(ics, "BEGIN:VEVENT") != 1 {
		t.Fatalf("expected single vevent, got %s", ics)
	}
	if evs[0].UID != "uid-week" {
		t.Fatalf("uid %s", evs[0].UID)
	}
}

func TestCollapseDeletedOccurrenceIsExdate(t *testing.T) {
	start := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	var rows []ekOccurrence
	for i := 0; i < 4; i++ {
		if i == 2 {
			continue
		}
		occ := start.AddDate(0, 0, 7*i)
		rows = append(rows, ekOccurrence{
			ID: "series-1", EventID: "series-1", CalendarID: "cal", UID: "uid-week",
			Title: "Standup", Start: float64(occ.Unix()), End: float64(occ.Add(time.Hour).Unix()),
			Occurrence: float64(occ.Unix()), MasterStart: float64(start.Unix()),
			MasterEnd: float64(start.Add(time.Hour).Unix()),
			RRule:     "FREQ=WEEKLY;BYDAY=MO",
		})
	}
	evs, err := collapseEKEvents(rows, start.Add(-time.Hour), start.AddDate(0, 0, 28))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d", len(evs))
	}
	if !strings.Contains(string(evs[0].ICS), "EXDATE") {
		t.Fatalf("missing exdate: %s", evs[0].ICS)
	}
}

func TestCollapseDetachedIsRecurrenceID(t *testing.T) {
	start := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	moved := start.AddDate(0, 0, 7).Add(time.Hour)
	orig := start.AddDate(0, 0, 7)
	rows := []ekOccurrence{
		{
			ID: "series-1", EventID: "series-1", CalendarID: "cal", UID: "uid-week",
			Title: "Standup", Start: float64(start.Unix()), End: float64(start.Add(time.Hour).Unix()),
			Occurrence: float64(start.Unix()), MasterStart: float64(start.Unix()),
			MasterEnd: float64(start.Add(time.Hour).Unix()),
			RRule:     "FREQ=WEEKLY;BYDAY=MO",
		},
		{
			ID: "series-1", EventID: "series-1", CalendarID: "cal", UID: "uid-week",
			Title: "Standup", Start: float64(start.AddDate(0, 0, 14).Unix()),
			End:        float64(start.AddDate(0, 0, 14).Add(time.Hour).Unix()),
			Occurrence: float64(start.AddDate(0, 0, 14).Unix()), MasterStart: float64(start.Unix()),
			MasterEnd: float64(start.Add(time.Hour).Unix()),
			RRule:     "FREQ=WEEKLY;BYDAY=MO",
		},
		{
			ID: "det-1", EventID: "series-1/RID=2", CalendarID: "cal", UID: "other",
			Title: "Standup moved", Start: float64(moved.Unix()), End: float64(moved.Add(time.Hour).Unix()),
			Occurrence: float64(orig.Unix()), Detached: 1,
		},
	}
	evs, err := collapseEKEvents(rows, start.Add(-time.Hour), start.AddDate(0, 0, 16))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d", len(evs))
	}
	ics := string(evs[0].ICS)
	if !strings.Contains(ics, "RECURRENCE-ID") {
		t.Fatalf("missing recurrence-id: %s", ics)
	}
	if !strings.Contains(ics, "Standup moved") {
		t.Fatalf("missing exception summary: %s", ics)
	}
	if strings.Count(ics, "BEGIN:VEVENT") != 2 {
		t.Fatalf("want master+exception: %s", ics)
	}
}

func TestEncodeRecurringICSRoundTrip(t *testing.T) {
	start := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	ics, err := EncodeEventICS(EventSpec{
		UID: "uid-r", Summary: "Weekly", Start: start, End: start.Add(time.Hour),
		RRule:   "FREQ=WEEKLY;BYDAY=MO",
		ExDates: []time.Time{start.AddDate(0, 0, 7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseEventICS(ics)
	if err != nil {
		t.Fatal(err)
	}
	if spec.UID != "uid-r" || spec.Summary != "Weekly" {
		t.Fatalf("%+v", spec)
	}
	if spec.RRule == "" || !strings.Contains(spec.RRule, "WEEKLY") {
		t.Fatalf("rrule %q", spec.RRule)
	}
	if UIDFromICS(ics) != "uid-r" {
		t.Fatal(UIDFromICS(ics))
	}
}

func TestEncodeEventICSKeepsLocalTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2020, 1, 6, 0, 30, 0, 0, loc) // Monday 00:30 Oslo = Sunday UTC
	ics, err := EncodeEventICS(EventSpec{
		UID: "uid-tz", Summary: "Nightly", TZ: "Europe/Oslo",
		Start: start, End: start.Add(time.Hour), RRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(ics)
	if !strings.Contains(s, "TZID=Europe/Oslo") {
		t.Fatalf("expected TZID: %s", s)
	}
	if strings.Contains(s, "DTSTART:20200105T") {
		t.Fatalf("stored as UTC Sunday: %s", s)
	}
	spec, err := ParseEventICS(ics)
	if err != nil {
		t.Fatal(err)
	}
	if spec.TZ != "Europe/Oslo" {
		t.Fatalf("tz %q", spec.TZ)
	}
	if spec.Start.Weekday() != time.Monday {
		t.Fatalf("weekday %s", spec.Start.Weekday())
	}
	if !strings.Contains(s, "BEGIN:VTIMEZONE") || !strings.Contains(s, "TZID:Europe/Oslo") {
		t.Fatalf("missing vtimezone: %s", s)
	}
}

func TestEncodeEventICSAlarmsAttendeesURL(t *testing.T) {
	start := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	ics, err := EncodeEventICS(EventSpec{
		UID: "uid-full", Summary: "Standup", Start: start, End: start.Add(time.Hour),
		URL: "https://meet.example/standup", Status: "CONFIRMED", Transp: "OPAQUE",
		Organizer: PersonSpec{Name: "Ada", Email: "ada@example.com"},
		Attendees: []PersonSpec{
			{Name: "Bob", Email: "bob@example.com", PartStat: "ACCEPTED", Role: "REQ-PARTICIPANT"},
		},
		Alarms: []AlarmSpec{{OffsetSec: -900, Desc: "Soon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(ics)
	for _, want := range []string{"BEGIN:VALARM", "TRIGGER:-PT900S", "ATTENDEE", "ORGANIZER", "mailto:ada@example.com", "URL:", "STATUS:CONFIRMED", "TRANSP:OPAQUE"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
	spec, err := ParseEventICS(ics)
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL == "" || spec.Organizer.Email == "" || len(spec.Attendees) != 1 || len(spec.Alarms) != 1 {
		t.Fatalf("%+v", spec)
	}
	if spec.Alarms[0].OffsetSec != -900 {
		t.Fatalf("offset %d", spec.Alarms[0].OffsetSec)
	}
}

func TestEncodeEventICSAllowsCRLFText(t *testing.T) {
	start := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	ics, err := EncodeEventICS(EventSpec{
		UID: "uid-crlf", Summary: "Title\rwith CR", Notes: "line1\r\nline2\rline3",
		Location: "Office\r\nFloor 2", Start: start, End: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseEventICS(ics)
	if err != nil {
		t.Fatal(err)
	}
	if spec.UID != "uid-crlf" {
		t.Fatalf("uid %q", spec.UID)
	}
	if !strings.Contains(spec.Notes, "line1") || !strings.Contains(spec.Notes, "line2") {
		t.Fatalf("notes %q", spec.Notes)
	}
}
