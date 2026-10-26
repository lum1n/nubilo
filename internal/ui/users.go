package ui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"nubilo/internal/identity"
	"nubilo/internal/qr"
	"nubilo/internal/server"
	"nubilo/internal/syncengine"
)

// actAsHeader selects which user the admin console acts as. Data views and
// writes (collections, uploads, photos, pairing, app passwords) apply to
// that user; without it the console acts as the default owner admin.
const actAsHeader = "X-Nubilo-Act-As"

func (s *Server) actAs(r *http.Request) (*identity.User, error) {
	id := strings.TrimSpace(r.Header.Get(actAsHeader))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("as"))
	}
	if id == "" {
		return s.RT.IDs.EnsureOwner(r.Context())
	}
	u, err := s.RT.IDs.GetUser(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !u.Active() {
		return nil, identity.ErrUserDisabled
	}
	return u, nil
}

// actingUser writes an error and returns nil when the act-as user is invalid.
func (s *Server) actingUser(w http.ResponseWriter, r *http.Request) *identity.User {
	u, err := s.actAs(r)
	if err != nil {
		http.Error(w, "unknown or disabled user", http.StatusBadRequest)
		return nil
	}
	return u
}

func (s *Server) operator(u *identity.User) *identity.Device {
	return syncengine.LocalOperatorAs(u)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Ensure the owner exists before listing so a fresh install shows it.
	owner, _ := s.RT.IDs.EnsureOwner(ctx)
	users, err := s.RT.IDs.ListUsers(ctx)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	usage, _ := s.RT.Engine.UserUsage(ctx)
	devs, _ := s.RT.IDs.List(ctx)
	perUser := map[string]int{}
	for _, d := range devs {
		if !d.Revoked() {
			perUser[d.UserID]++
		}
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		us := usage[u.ID]
		out = append(out, map[string]any{
			"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
			"role": u.Role, "status": u.Status, "created_at": u.CreatedAt,
			"devices": perUser[u.ID], "objects": us.Objects, "bytes": us.Bytes,
		})
	}
	def := ""
	if owner != nil {
		def = owner.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "default_user": def})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, err := s.RT.IDs.CreateUser(r.Context(), req.Username, req.DisplayName, identity.UserRole(req.Role))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, identity.ErrUserExists) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.RT.Audit.Event(r.Context(), "admin-local", "user.create", map[string]any{"user": u.ID})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Role        string `json:"role"`
		Status      string `json:"status"`
		DisplayName string `json:"display_name"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var err error
	if req.Role != "" {
		err = s.RT.IDs.SetUserRole(ctx, id, identity.UserRole(req.Role))
	}
	if err == nil && req.Status != "" {
		err = s.RT.IDs.SetUserStatus(ctx, id, req.Status)
	}
	if err == nil && req.DisplayName != "" {
		err = s.RT.IDs.SetDisplayName(ctx, id, req.DisplayName)
	}
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, identity.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, identity.ErrLastAdmin):
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	u, err := s.RT.IDs.GetUser(ctx, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.RT.Audit.Event(ctx, "admin-local", "user.update", map[string]any{"user": id, "role": u.Role, "status": u.Status})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleInvites(w http.ResponseWriter, r *http.Request) {
	list, err := s.RT.IDs.ListInvites(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []identity.Invite{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": list})
}

// publicBase is the URL devices use to reach the server: the public domain,
// else the LAN listen address.
func (s *Server) publicBase() string {
	return s.RT.Cfg.DeviceBaseURL()
}

func (s *Server) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role     string `json:"role"`
		UserID   string `json:"user_id"`
		TTLHours int    `json:"ttl_hours"`
		MaxUses  int    `json:"max_uses"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code, inv, err := s.RT.IDs.CreateInvite(r.Context(), "admin-local", identity.UserRole(req.Role), req.UserID,
		time.Duration(req.TTLHours)*time.Hour, req.MaxUses)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	base := s.publicBase()
	setup := server.SetupURL(base, code, b64url(s.RT.ServerPub))
	png, _ := qr.PNGDataURL(setup, 320)
	s.RT.Audit.Event(r.Context(), "admin-local", "invite.create", map[string]any{"invite": inv.ID, "role": inv.Role})
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         inv.ID,
		"code":       code,
		"url":        server.InviteURL(base, code),
		"setup_url":  setup,
		"qr":         png,
		"expires_at": inv.ExpiresAt,
		"role":       inv.Role,
		"user_id":    inv.UserID,
	})
}

func (s *Server) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.RT.IDs.RevokeInvite(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": r.PathValue("id")})
}
