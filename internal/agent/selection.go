package agent

import (
	"encoding/json"
	"os"
)

type FileFolderSel struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type FilesSel struct {
	Enabled bool            `json:"enabled"`
	Folders []FileFolderSel `json:"folders"`
}

// CalendarSel is a Mac calendar mirrored to the server.
type CalendarSel struct {
	LocalID string `json:"local_id"`
	Title   string `json:"title"`
}

type Selection struct {
	IntervalSeconds int `json:"interval_seconds"`
	// WindowDays bounds mirrored events to ±this many days around now.
	WindowDays int           `json:"window_days"`
	Calendars  []CalendarSel `json:"calendars"`
	Files      FilesSel      `json:"files"`
}

const defaultWindowDays = 730

func DefaultSelection() Selection {
	return Selection{IntervalSeconds: 120, WindowDays: defaultWindowDays}
}

func LoadSelection(path string) (Selection, error) {
	s := DefaultSelection()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.IntervalSeconds <= 0 {
		s.IntervalSeconds = 120
	}
	if s.WindowDays <= 0 {
		s.WindowDays = defaultWindowDays
	}
	return s, nil
}

func SaveSelection(path string, s Selection) error {
	if s.IntervalSeconds <= 0 {
		s.IntervalSeconds = 120
	}
	if s.WindowDays <= 0 {
		s.WindowDays = defaultWindowDays
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (s *Selection) AddFileFolder(path, name string) {
	for i := range s.Files.Folders {
		if s.Files.Folders[i].Path == path {
			if name != "" {
				s.Files.Folders[i].Name = name
			}
			return
		}
	}
	s.Files.Folders = append(s.Files.Folders, FileFolderSel{Path: path, Name: name})
}

func (s *Selection) RemoveFileFolder(path string) {
	out := s.Files.Folders[:0]
	for _, f := range s.Files.Folders {
		if f.Path != path {
			out = append(out, f)
		}
	}
	s.Files.Folders = out
}

// AddCalendar mirrors the Mac calendar id (title is its name in Calendar).
func (s *Selection) AddCalendar(id, title string) {
	for i := range s.Calendars {
		if s.Calendars[i].LocalID == id {
			s.Calendars[i].Title = title
			return
		}
	}
	s.Calendars = append(s.Calendars, CalendarSel{LocalID: id, Title: title})
}

// RemoveCalendar stops mirroring id. The copy on the server stays until it is deleted there.
func (s *Selection) RemoveCalendar(id string) {
	out := s.Calendars[:0]
	for _, c := range s.Calendars {
		if c.LocalID != id {
			out = append(out, c)
		}
	}
	s.Calendars = out
}
