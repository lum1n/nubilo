package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"nubilo/internal/update"
)

// Server updates for admins: see internal/update and docs/API.md.

func (s *Server) updater(w http.ResponseWriter, r *http.Request) *update.Manager {
	if s.requireAdmin(w, r) == nil {
		return nil
	}
	if s.Updater == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil
	}
	return s.Updater
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if u := s.updater(w, r); u != nil {
		s.writeJSON(w, http.StatusOK, u.Status())
	}
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	u := s.updater(w, r)
	if u == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st, err := u.Check(ctx)
	if err != nil {
		http.Error(w, "could not reach the release server: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	u := s.updater(w, r)
	if u == nil {
		return
	}
	dev := deviceFrom(r)
	st, err := u.StartInstall()
	if errors.Is(err, update.ErrBusy) {
		s.writeJSON(w, http.StatusAccepted, st)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.Log.Info("update_requested", "by", dev.ID, "to", versionOf(st.Available))
	s.writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	u := s.updater(w, r)
	if u == nil {
		return
	}
	var req struct {
		Auto *bool `json:"auto"`
	}
	if err := s.readJSON(r, &req); err != nil || req.Auto == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	st, err := u.SetAuto(*req.Auto)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func versionOf(r *update.Release) string {
	if r == nil {
		return ""
	}
	return r.Version
}
