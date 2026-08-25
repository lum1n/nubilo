package ui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"nubilo/internal/agent"
	"nubilo/internal/version"
)

func (s *AgentServer) handleOverview(w http.ResponseWriter, r *http.Request) {
	sel, err := s.loadSel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pair, _ := agent.ReadPairingInfo(s.DataDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         version.String,
		"data_dir":        s.DataDir,
		"ui_listen":       s.Listen,
		"pairing":         pair,
		"photos_auth":     agent.PhotosAuthStatus(),
		"interval_seconds": sel.IntervalSeconds,
		"window_days":     sel.WindowDays,
		"counts": map[string]any{
			"calendars": len(sel.Calendars),
			"reminders": len(sel.Reminders),
			"albums":    len(sel.Photos.Albums),
			"folders":   len(sel.Files.Folders),
		},
		"sync_contacts": sel.SyncContacts,
		"photos_enabled": sel.Photos.Enabled,
		"files_enabled": sel.Files.Enabled,
	})
}

func (s *AgentServer) handleSelectionGet(w http.ResponseWriter, r *http.Request) {
	sel, err := s.loadSel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sel)
}

func (s *AgentServer) handleSelectionPut(w http.ResponseWriter, r *http.Request) {
	var sel agent.Selection
	if err := readJSON(r, &sel); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := agent.SaveSelection(s.SelPath, sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	got, err := agent.LoadSelection(s.SelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *AgentServer) handleCalendars(w http.ResponseWriter, r *http.Request) {
	sel, err := s.loadSel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chosen := map[string]bool{}
	for _, c := range sel.Calendars {
		chosen[c.LocalID] = true
	}
	list, err := agent.PlatformCalendars()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	rows := make([]map[string]any, 0, len(list))
	for _, c := range list {
		rows = append(rows, map[string]any{
			"id":       c.ID,
			"title":    c.Title,
			"color":    c.Color,
			"selected": chosen[c.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendars": rows})
}

func (s *AgentServer) handleCalendarSelect(w http.ResponseWriter, r *http.Request) {
	s.toggleCalendar(w, r, true)
}

func (s *AgentServer) handleCalendarUnselect(w http.ResponseWriter, r *http.Request) {
	s.toggleCalendar(w, r, false)
}

func (s *AgentServer) toggleCalendar(w http.ResponseWriter, r *http.Request, on bool) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sel, err := agent.LoadSelection(s.SelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if on {
		title := req.ID
		if list, err := agent.PlatformCalendars(); err == nil {
			for _, c := range list {
				if c.ID == req.ID {
					title = c.Title
					break
				}
			}
		}
		sel.SelectCalendar(req.ID, title)
	} else {
		sel.UnselectCalendar(req.ID)
	}
	if err := agent.SaveSelection(s.SelPath, sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *AgentServer) handleReminders(w http.ResponseWriter, r *http.Request) {
	sel, err := s.loadSel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chosen := map[string]bool{}
	for _, c := range sel.Reminders {
		chosen[c.LocalID] = true
	}
	list, err := agent.PlatformReminderLists()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	rows := make([]map[string]any, 0, len(list))
	for _, c := range list {
		rows = append(rows, map[string]any{
			"id":       c.ID,
			"title":    c.Title,
			"color":    c.Color,
			"selected": chosen[c.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": rows})
}

func (s *AgentServer) handleReminderSelect(w http.ResponseWriter, r *http.Request) {
	s.toggleReminder(w, r, true)
}

func (s *AgentServer) handleReminderUnselect(w http.ResponseWriter, r *http.Request) {
	s.toggleReminder(w, r, false)
}

func (s *AgentServer) toggleReminder(w http.ResponseWriter, r *http.Request, on bool) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sel, err := agent.LoadSelection(s.SelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if on {
		title := req.ID
		if list, err := agent.PlatformReminderLists(); err == nil {
			for _, c := range list {
				if c.ID == req.ID {
					title = c.Title
					break
				}
			}
		}
		sel.SelectReminder(req.ID, title)
	} else {
		sel.UnselectReminder(req.ID)
	}
	if err := agent.SaveSelection(s.SelPath, sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *AgentServer) handleAlbums(w http.ResponseWriter, r *http.Request) {
	sel, err := s.loadSel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chosen := map[string]bool{}
	for _, id := range sel.Photos.Albums {
		chosen[id] = true
	}
	libraryCount, albums, err := agent.PlatformAlbumList()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	rows := make([]map[string]any, 0, len(albums))
	for _, a := range albums {
		kind := a.Kind
		if kind == "" {
			kind = "user"
		}
		rows = append(rows, map[string]any{
			"id":       a.ID,
			"title":    a.Title,
			"kind":     kind,
			"count":    a.Count,
			"selected": chosen[a.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"library_count": libraryCount,
		"photos_auth":   agent.PhotosAuthStatus(),
		"albums":        rows,
	})
}

func (s *AgentServer) handleAlbumSelect(w http.ResponseWriter, r *http.Request) {
	s.toggleAlbum(w, r, true)
}

func (s *AgentServer) handleAlbumUnselect(w http.ResponseWriter, r *http.Request) {
	s.toggleAlbum(w, r, false)
}

func (s *AgentServer) toggleAlbum(w http.ResponseWriter, r *http.Request, on bool) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sel, err := agent.LoadSelection(s.SelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if on {
		sel.SelectAlbum(req.ID)
	} else {
		sel.UnselectAlbum(req.ID)
	}
	if err := agent.SaveSelection(s.SelPath, sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *AgentServer) handlePhotosAuthorize(w http.ResponseWriter, r *http.Request) {
	status, err := agent.AuthorizePhotosViaApp()
	if err != nil {
		status2, err2 := agent.RequestPhotosAccess()
		if err2 != nil {
			http.Error(w, err2.Error(), http.StatusBadGateway)
			return
		}
		status = status2
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (s *AgentServer) handleFilesAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p := absPath(strings.TrimSpace(req.Path))
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() {
		http.Error(w, "path must be an existing directory", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sel, err := agent.LoadSelection(s.SelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sel.AddFileFolder(p, name)
	if err := agent.SaveSelection(s.SelPath, sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": p, "name": name})
}

func (s *AgentServer) handleFilesRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sel, err := agent.LoadSelection(s.SelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sel.RemoveFileFolder(req.Path)
	if err := agent.SaveSelection(s.SelPath, sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *AgentServer) handlePair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server   string `json:"server"`
		Code     string `json:"code"`
		Name     string `json:"name"`
		Insecure bool   `json:"insecure"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := agent.PairWithServer(s.DataDir, req.Server, req.Code, req.Name, req.Insecure)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device_id": id})
}

func (s *AgentServer) loadSel() (agent.Selection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return agent.LoadSelection(s.SelPath)
}
