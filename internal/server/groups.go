package server

import (
	"errors"
	"net/http"

	"nubilo/internal/identity"
)

// handleGroups lists every group with its members, for share pickers. Like
// the user directory it is visible to every user.
func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	if dev := deviceFrom(r); dev == nil || dev.Revoked() || dev.UserID == "" && !dev.System() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	groups, err := s.IDs.ListGroups(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

type groupReq struct {
	Name    string    `json:"name"`
	Members *[]string `json:"members"` // user IDs; absent leaves them
}

func (s *Server) groupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		http.Error(w, "unknown group or user", http.StatusNotFound)
	case errors.Is(err, identity.ErrGroupExists):
		http.Error(w, "group name taken", http.StatusConflict)
	case errors.Is(err, identity.ErrGroupName):
		http.Error(w, "invalid group name", http.StatusBadRequest)
	default:
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}

func (s *Server) handleAdminCreateGroup(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	var req groupReq
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var members []string
	if req.Members != nil {
		members = *req.Members
	}
	g, err := s.IDs.CreateGroup(r.Context(), req.Name, members)
	if err != nil {
		s.groupError(w, err)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "group.create", map[string]any{"group": g.ID, "members": g.Members})
	s.writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleAdminPatchGroup(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	var req groupReq
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var members []string
	if req.Members != nil {
		members = append([]string{}, *req.Members...)
	}
	g, err := s.IDs.UpdateGroup(r.Context(), r.PathValue("id"), req.Name, members)
	if err != nil {
		s.groupError(w, err)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "group.update", map[string]any{"group": g.ID, "members": g.Members})
	s.writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	id := r.PathValue("id")
	if err := s.IDs.DeleteGroup(r.Context(), id); err != nil {
		s.groupError(w, err)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "group.delete", map[string]any{"group": id})
	w.WriteHeader(http.StatusNoContent)
}
