package agent

import (
	"errors"
	"time"
)

var (
	ErrNotPaired  = errors.New("agent: device is not paired (run nubilo pair --server …)")
	ErrNeedDarwin = errors.New("agent: calendar mirroring requires macOS (EventKit)")
)

// CalendarInfo is one calendar in the Mac's Calendar app.
type CalendarInfo struct {
	ID    string
	Title string
	Color string
}

// LocalEvent is one event (a whole recurring series) as ICS.
type LocalEvent struct {
	ID         string
	CalendarID string
	UID        string
	ICS        []byte
	StartMS    int64
}

// CalendarSource reads the Mac's calendars. The agent only copies them to
// the server; it never writes to them.
type CalendarSource interface {
	ListCalendars() ([]CalendarInfo, error)
	ListEvents(calendarID string, start, end time.Time) ([]LocalEvent, error)
}
