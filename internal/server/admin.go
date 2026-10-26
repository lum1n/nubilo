package server

import (
	"errors"
	"net/http"
	"time"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
)

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *identity.Device {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.UserAdmin); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	return dev
}

type adminUserRow struct {
	identity.User
	Devices int   `json:"devices"`
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

func (s *Server) adminUserRows(r *http.Request) ([]adminUserRow, error) {
	users, err := s.IDs.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	usage, err := s.Engine.UserUsage(r.Context())
	if err != nil {
		return nil, err
	}
	devs, err := s.IDs.List(r.Context())
	if err != nil {
		return nil, err
	}
	perUser := map[string]int{}
	for _, d := range devs {
		if !d.Revoked() {
			perUser[d.UserID]++
		}
	}
	out := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		us := usage[u.ID]
		out = append(out, adminUserRow{User: u, Devices: perUser[u.ID], Objects: us.Objects, Bytes: us.Bytes})
	}
	return out, nil
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	rows, err := s.adminUserRows(r)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": rows})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	rows, err := s.adminUserRows(r)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	blobs, blobBytes, _ := s.Store.BlobStats(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"users":      rows,
		"blobs":      blobs,
		"blob_bytes": blobBytes,
	})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	var req struct {
		Username    string            `json:"username"`
		DisplayName string            `json:"display_name"`
		Role        identity.UserRole `json:"role"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, err := s.IDs.CreateUser(r.Context(), req.Username, req.DisplayName, req.Role)
	if errors.Is(err, identity.ErrUserExists) {
		http.Error(w, "username taken", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "user.create", map[string]any{"user": u.ID, "role": u.Role})
	s.writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleAdminPatchUser(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Role        identity.UserRole `json:"role"`
		Status      string            `json:"status"`
		DisplayName string            `json:"display_name"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fail := func(err error) {
		switch {
		case errors.Is(err, identity.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, identity.ErrLastAdmin):
			http.Error(w, "cannot remove the last admin", http.StatusConflict)
		default:
			http.Error(w, "bad request", http.StatusBadRequest)
		}
	}
	if req.Role != "" {
		if err := s.IDs.SetUserRole(r.Context(), id, req.Role); err != nil {
			fail(err)
			return
		}
	}
	if req.Status != "" {
		if err := s.IDs.SetUserStatus(r.Context(), id, req.Status); err != nil {
			fail(err)
			return
		}
	}
	if req.DisplayName != "" {
		if err := s.IDs.SetDisplayName(r.Context(), id, req.DisplayName); err != nil {
			fail(err)
			return
		}
	}
	u, err := s.IDs.GetUser(r.Context(), id)
	if err != nil {
		fail(err)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "user.update", map[string]any{"user": id, "role": u.Role, "status": u.Status})
	s.writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	list, err := s.IDs.ListInvites(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []identity.Invite{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"invites": list})
}

func (s *Server) handleAdminCreateInvite(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	var req struct {
		Role     identity.UserRole `json:"role"`
		UserID   string            `json:"user_id"`
		TTLHours int               `json:"ttl_hours"`
		MaxUses  int               `json:"max_uses"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	by := dev.UserID
	if by == "" {
		by = dev.ID
	}
	code, inv, err := s.IDs.CreateInvite(r.Context(), by, req.Role, req.UserID, time.Duration(req.TTLHours)*time.Hour, req.MaxUses)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "invite.create", map[string]any{"invite": inv.ID, "role": inv.Role, "for_user": inv.UserID})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id":         inv.ID,
		"code":       code,
		"url":        InviteURL(s.baseURL(r), code),
		"setup_url":  SetupURL(s.baseURL(r), code, b64url(s.ServerPub)),
		"expires_at": inv.ExpiresAt,
		"role":       inv.Role,
		"max_uses":   inv.MaxUses,
	})
}

// InviteURL is the link shown as a QR code for members: a landing page that
// hands the code (kept in the fragment) to the app.
func InviteURL(base, code string) string {
	return base + "/join#" + code
}

// SetupURL is the app deep link with the server URL, invite and server key
// fingerprint (for pinning on first contact).
func SetupURL(base, code, serverKey string) string {
	return "nubilo://setup?url=" + queryEscape(base) + "&invite=" + queryEscape(code) + "&fp=" + queryEscape(serverKey)
}

func (s *Server) handleAdminRevokeInvite(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	id := r.PathValue("id")
	if err := s.IDs.RevokeInvite(r.Context(), id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "invite.revoke", map[string]any{"invite": id})
	s.writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

func (s *Server) handleAdminRevokeDevice(w http.ResponseWriter, r *http.Request) {
	dev := s.requireAdmin(w, r)
	if dev == nil {
		return
	}
	id := r.PathValue("id")
	if err := s.IDs.Revoke(r.Context(), id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "device.revoke", map[string]any{"target": id})
	s.writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}
