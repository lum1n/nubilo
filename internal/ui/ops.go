package ui

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nubilo/internal/app"
	"nubilo/internal/backup"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/integrity"
	"nubilo/internal/version"
)

type dlToken struct {
	path    string
	expires time.Time
}

func (s *Server) ensureBackupTok() {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if s.backupTok == nil {
		s.backupTok = map[string]dlToken{}
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, _ := s.RT.Engine.HeadSeq(ctx)
	devs, _ := s.RT.IDs.List(ctx)
	active, revoked := 0, 0
	for _, d := range devs {
		if d.Revoked() {
			revoked++
		} else {
			active++
		}
	}
	blobCount, blobBytes, _ := s.RT.Store.BlobStats(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":                version.String,
		"data_dir":               s.RT.Cfg.DataDir,
		"listen":                 s.RT.Cfg.Listen,
		"head_seq":               head,
		"devices":                active,
		"devices_revoked":        revoked,
		"blob_count":             blobCount,
		"blob_bytes":             blobBytes,
		"tls_cert":               s.RT.Cfg.TLS.Cert,
		"tls_key":                s.RT.Cfg.TLS.Key,
		"tls_auto":               s.RT.Cfg.TLS.Auto,
		"backup_enabled":         s.RT.Cfg.Backup.Enabled,
		"backup_interval_h":      s.RT.Cfg.Backup.IntervalHours,
		"backup_keep":            s.RT.Cfg.Backup.Keep,
		"backup_passphrase_file": s.RT.Cfg.Backup.PassphraseFile,
		"last_backup_unix_ms":    s.RT.Cfg.Backup.LastBackupUnixMS,
		"last_backup_error":      s.RT.Cfg.Backup.LastBackupError,
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

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Passphrase) == "" {
		http.Error(w, "passphrase required", http.StatusBadRequest)
		return
	}
	tmpDir := filepath.Join(s.RT.Cfg.DataDir, "tmp")
	_ = os.MkdirAll(tmpDir, 0o700)
	dest := filepath.Join(tmpDir, "ui-backup-"+hex.EncodeToString(randBytes(8))+".nuback")
	if err := backup.Create(r.Context(), s.RT.Store, s.RT.Cfg.DataDir, dest, strings.TrimSpace(req.Passphrase)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tok := hex.EncodeToString(randBytes(16))
	s.ensureBackupTok()
	s.backupMu.Lock()
	s.backupTok[tok] = dlToken{path: dest, expires: time.Now().Add(10 * time.Minute)}
	s.backupMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    tok,
		"download": "/api/backup/download/" + tok,
		"expires":  "10m",
	})
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	s.ensureBackupTok()
	s.backupMu.Lock()
	entry, ok := s.backupTok[tok]
	if ok {
		delete(s.backupTok, tok)
	}
	s.backupMu.Unlock()
	if !ok || time.Now().After(entry.expires) {
		if entry.path != "" {
			_ = os.Remove(entry.path)
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer os.Remove(entry.path)
	f, err := os.Open(entry.path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="nubilo-backup.nuback"`)
	if st != nil {
		w.Header().Set("Content-Length", formatInt(st.Size()))
	}
	_, _ = io.Copy(w, f)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "multipart required", http.StatusBadRequest)
		return
	}
	pass := strings.TrimSpace(r.FormValue("passphrase"))
	dest := strings.TrimSpace(r.FormValue("dest"))
	confirm := strings.TrimSpace(r.FormValue("confirm"))
	if pass == "" || dest == "" {
		http.Error(w, "passphrase and dest required", http.StatusBadRequest)
		return
	}
	if confirm != "RESTORE" {
		http.Error(w, `confirm must be exactly "RESTORE"`, http.StatusBadRequest)
		return
	}
	same, err := backup.SameDataDir(s.RT.Cfg.DataDir, dest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if same {
		http.Error(w, "refusing restore onto live data_dir; stop server and use CLI: nubilo restore", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("archive")
	if err != nil {
		http.Error(w, "archive file required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	tmp, err := os.CreateTemp(filepath.Join(s.RT.Cfg.DataDir, "tmp"), "restore-*.nuback")
	if err != nil {
		_ = os.MkdirAll(filepath.Join(s.RT.Cfg.DataDir, "tmp"), 0o700)
		tmp, err = os.CreateTemp(filepath.Join(s.RT.Cfg.DataDir, "tmp"), "restore-*.nuback")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp.Close()
	if err := backup.Restore(context.Background(), tmpPath, dest, pass); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dest": dest})
}

func (s *Server) handleTLSRegen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hosts string `json:"hosts"`
	}
	_ = readJSON(r, &req)
	var extra []string
	for _, h := range strings.Split(req.Hosts, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			extra = append(extra, h)
		}
	}
	cfg := s.RT.Cfg
	if err := app.RegenerateTLS(&cfg, extra); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cfg.DataDir = s.RT.Cfg.DataDir
	if err := cfg.Save(s.RT.Paths.Config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.RT.Cfg = cfg
	writeJSON(w, http.StatusOK, map[string]any{
		"cert":    cfg.TLS.Cert,
		"key":     cfg.TLS.Key,
		"restart": "restart nubilo server to apply TLS",
	})
}

func (s *Server) handleDeviceEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		PubKey string `json:"pubkey"`
		Role   string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.PubKey) == "" {
		http.Error(w, "name and pubkey required", http.StatusBadRequest)
		return
	}
	pub, err := parseDevicePub(req.PubKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	role := identity.Role(strings.TrimSpace(req.Role))
	if role == "" {
		role = identity.RoleClient
	}
	dev, err := s.RT.IDs.Enroll(r.Context(), req.Name, pub, role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": dev.ID, "name": dev.Name, "role": string(dev.Role)})
}

func (s *Server) handleDeviceRotate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		PubKey string `json:"pubkey"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ID == "" || strings.TrimSpace(req.PubKey) == "" {
		http.Error(w, "id and pubkey required", http.StatusBadRequest)
		return
	}
	pub, err := parseDevicePub(req.PubKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.RT.IDs.RotatePublicKey(r.Context(), req.ID, pub); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": req.ID})
}

func parseDevicePub(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	raw := []byte(s)
	if len(raw) == 32 {
		return raw, nil
	}
	return nil, errBadPub
}

var errBadPub = errString("pubkey must be 32 raw bytes, hex, or base64")

type errString string

func (e errString) Error() string { return string(e) }

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func formatInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
