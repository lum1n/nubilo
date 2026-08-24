package agent

import (
	"encoding/json"
	"os"
)

type CalendarSel struct {
	LocalID string `json:"local_id"`
	Title   string `json:"title"`
}

type PhotosSel struct {
	Enabled  bool     `json:"enabled"`
	Source   string   `json:"source"`
	Albums   []string `json:"albums"`
	AfterMS  int64    `json:"after_ms"`
	BeforeMS int64    `json:"before_ms"`
}

type Selection struct {
	IntervalSeconds int           `json:"interval_seconds"`
	WindowDays      int           `json:"window_days"`
	Calendars       []CalendarSel `json:"calendars"`
	SyncContacts    bool          `json:"sync_contacts"`
	Photos          PhotosSel     `json:"photos"`
}

func DefaultSelection() Selection {
	return Selection{IntervalSeconds: 120, WindowDays: 730}
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
		s.WindowDays = 730
	}
	if s.Photos.Source == "" {
		s.Photos.Source = "all"
	}
	return s, nil
}

func SaveSelection(path string, s Selection) error {
	if s.IntervalSeconds <= 0 {
		s.IntervalSeconds = 120
	}
	if s.WindowDays <= 0 {
		s.WindowDays = 730
	}
	if s.Photos.Source == "" {
		s.Photos.Source = "all"
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (s *Selection) SelectCalendar(id, title string) {
	for i := range s.Calendars {
		if s.Calendars[i].LocalID == id {
			s.Calendars[i].Title = title
			return
		}
	}
	s.Calendars = append(s.Calendars, CalendarSel{LocalID: id, Title: title})
}

func (s *Selection) UnselectCalendar(id string) {
	out := s.Calendars[:0]
	for _, c := range s.Calendars {
		if c.LocalID != id {
			out = append(out, c)
		}
	}
	s.Calendars = out
}

func (s *Selection) SelectAlbum(id string) {
	for _, a := range s.Photos.Albums {
		if a == id {
			return
		}
	}
	s.Photos.Albums = append(s.Photos.Albums, id)
}

func (s *Selection) UnselectAlbum(id string) {
	out := s.Photos.Albums[:0]
	for _, a := range s.Photos.Albums {
		if a != id {
			out = append(out, a)
		}
	}
	s.Photos.Albums = out
}

func (s Selection) PhotoFilter() PhotoFilter {
	src := s.Photos.Source
	if src == "" {
		src = "all"
	}
	return PhotoFilter{Source: src, Albums: s.Photos.Albums, AfterMS: s.Photos.AfterMS, BeforeMS: s.Photos.BeforeMS}
}
