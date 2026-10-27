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
		"version":          version.String,
		"data_dir":         s.DataDir,
		"ui_listen":        s.Listen,
		"pairing":          pair,
		"interval_seconds": sel.IntervalSeconds,
		"counts": map[string]any{
			"folders": len(sel.Files.Folders),
		},
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
