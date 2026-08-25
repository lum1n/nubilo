package agent

import (
	"errors"
	"time"
)

var (
	ErrNeedDarwin = errors.New("agent: EventKit, Contacts, and PhotoKit require macOS")
	ErrNotPaired  = errors.New("agent: device is not paired (run nubilo pair --server …)")
)

type CalendarInfo struct {
	ID    string
	Title string
	Color string
}

type LocalEvent struct {
	ID         string
	CalendarID string
	UID        string
	ICS        []byte
	StartMS    int64
}

type LocalTodo struct {
	ID     string
	ListID string
	UID    string
	ICS    []byte
	DueMS  int64 // due or completed; 0 = incomplete without a date
}

type LocalContact struct {
	ID    string
	UID   string
	VCard []byte
}

type PhotoInfo struct {
	ID    string
	Title string
	Count int // assets visible under current Photos authorization
}

type LocalPhoto struct {
	ID         string
	Filename   string
	Kind       string // image | video | live | raw
	TakenAtMS  int64
	ModMS      int64
	DurationMS int64
	Size       int64
	Albums     []string
	Original   []byte
	LiveMovie  []byte // paired Live Photo movie when Kind=live
}

type PhotoFilter struct {
	Source   string
	Albums   []string
	AfterMS  int64
	BeforeMS int64
}

type PhotoSource interface {
	ListAlbums() ([]PhotoInfo, error)
	ListPhotos(filter PhotoFilter) ([]LocalPhoto, error)
	ReadOriginal(id string) ([]byte, error)
	ReadLiveMovie(id string) ([]byte, error) // nil,nil if not a Live Photo
	ImportOriginal(data []byte, filename, albumID string) (localID string, err error)
}

type CalendarSource interface {
	ListCalendars() ([]CalendarInfo, error)
	ListEvents(calendarID string, start, end time.Time) ([]LocalEvent, error)
	UpsertEvent(calendarID, localID string, ics []byte) (string, error)
	DeleteEvent(localID string) error
}

type ReminderSource interface {
	ListReminderLists() ([]CalendarInfo, error)
	ListReminders(listID string, start, end time.Time) ([]LocalTodo, error)
	UpsertReminder(listID, localID string, ics []byte) (string, error)
	DeleteReminder(localID string) error
}

type ContactSource interface {
	ListContacts() ([]LocalContact, error)
	UpsertContact(localID string, vcf []byte) (string, error)
	DeleteContact(localID string) error
}
