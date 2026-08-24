package agent

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeParseTodoICS(t *testing.T) {
	due := time.Date(2026, 8, 25, 15, 0, 0, 0, time.UTC)
	done := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	ics, err := EncodeTodoICS(TodoSpec{
		UID: "todo-1", Summary: "Buy milk", Notes: "oat", Due: due, Completed: done,
		Status: "COMPLETED", Percent: 100, Priority: 5, URL: "https://example.com/milk",
		Alarms: []AlarmSpec{{OffsetSec: -3600, Desc: "Soon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(ics)
	for _, want := range []string{"BEGIN:VTODO", "SUMMARY:Buy milk", "PERCENT-COMPLETE:100", "STATUS:COMPLETED", "PRIORITY:5", "BEGIN:VALARM"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
	got, err := ParseTodoICS(ics)
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != "todo-1" || got.Summary != "Buy milk" || got.Percent != 100 || got.Priority != 5 {
		t.Fatalf("%+v", got)
	}
	if got.Due.UTC().Unix() != due.Unix() {
		t.Fatalf("due %v", got.Due)
	}
	if got.Completed.IsZero() {
		t.Fatal("completed")
	}
	if len(got.Alarms) != 1 {
		t.Fatalf("alarms %+v", got.Alarms)
	}
}
