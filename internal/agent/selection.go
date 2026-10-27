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

type Selection struct {
	IntervalSeconds int      `json:"interval_seconds"`
	Files           FilesSel `json:"files"`
}

func DefaultSelection() Selection {
	return Selection{IntervalSeconds: 120}
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
	return s, nil
}

func SaveSelection(path string, s Selection) error {
	if s.IntervalSeconds <= 0 {
		s.IntervalSeconds = 120
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
