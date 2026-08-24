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
