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
}

type LocalEvent struct {
	ID         string
	CalendarID string
	UID        string
	ICS        []byte
	StartMS    int64
}

type LocalContact struct {
	ID    string
	UID   string
	VCard []byte
}

type PhotoInfo struct {
	ID    string
	Title string
}

type LocalPhoto struct {
	ID        string
	Filename  string
	TakenAtMS int64
	ModMS     int64
	Size      int64
	Albums    []string
	Original  []byte
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
}

type CalendarSource interface {
	ListCalendars() ([]CalendarInfo, error)
	ListEvents(calendarID string, start, end time.Time) ([]LocalEvent, error)
	UpsertEvent(calendarID, localID string, ics []byte) (string, error)
	DeleteEvent(localID string) error
}

type ContactSource interface {
	ListContacts() ([]LocalContact, error)
	UpsertContact(localID string, vcf []byte) (string, error)
	DeleteContact(localID string) error
}
