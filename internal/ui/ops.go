package ui

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/integrity"
	"nubilo/internal/version"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, _ := s.RT.Engine.HeadSeq(ctx)
	devs, _ := s.RT.IDs.List(ctx)
	active := 0
	for _, d := range devs {
		if !d.Revoked() {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  version.String,
		"data_dir": s.RT.Cfg.DataDir,
		"listen":   s.RT.Cfg.Listen,
		"head_seq": head,
		"devices":  active,
		"tls_cert": s.RT.Cfg.TLS.Cert,
		"tls_key":  s.RT.Cfg.TLS.Key,
		"tls_auto": s.RT.Cfg.TLS.Auto,
	})
}

func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	_ = readJSON(r, &req)
	role := identity.Role(strings.TrimSpace(req.Role))
	if role == "" {
		role = identity.RoleAgent
	}
	switch role {
	case identity.RoleAgent, identity.RoleClient:
	default:
		http.Error(w, "role must be agent or client", http.StatusBadRequest)
		return
	}
	code, sess, err := s.RT.IDs.StartPairing(r.Context(), role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         sess.ID,
		"code":       ncrypto.FormatPairingCode(code),
		"role":       string(role),
		"expires_at": sess.ExpiresAt,
		"expires":    time.UnixMilli(sess.ExpiresAt).Format(time.RFC3339),
	})
}

func (s *Server) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var completed sql.NullInt64
	var deviceID sql.NullString
	var expires int64
	err := s.RT.Store.DB.QueryRowContext(r.Context(), `
		SELECT completed_at, device_id, expires_at FROM pairing_sessions WHERE id = ?
	`, id).Scan(&completed, &deviceID, &expires)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := map[string]any{
		"id":         id,
		"completed":  completed.Valid,
		"expires_at": expires,
		"expired":    !completed.Valid && time.Now().UnixMilli() > expires,
	}
	if deviceID.Valid {
		out["device_id"] = deviceID.String
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repair bool `json:"repair"`
	}
	_ = readJSON(r, &req)
	ctx := r.Context()
	issues, err := integrity.Check(ctx, s.RT.Store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := map[string]any{"ok": len(issues) == 0, "issues": issues}
	if req.Repair {
		orphans, refs, err := integrity.Repair(ctx, s.RT.Store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out["orphans_removed"] = orphans
		out["refcounts_repaired"] = refs
		issues, err = integrity.Check(ctx, s.RT.Store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out["ok"] = len(issues) == 0
		out["issues"] = issues
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Apply bool `json:"apply"`
	}
	_ = readJSON(r, &req)
	rep, err := integrity.Collect(r.Context(), s.RT.Store, req.Apply)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
