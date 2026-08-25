package ui

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/ids"
	"nubilo/internal/photos"
	"nubilo/internal/syncengine"
	"nubilo/internal/version"
)

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, _ := s.RT.Engine.HeadSeq(ctx)
	counts := map[string]int{}
	for _, kind := range []string{"calendar", "addressbook", "files", "photos"} {
		cols, err := s.RT.Engine.ChildCollections(ctx, kind, "")
		if err != nil {
			continue
		}
		n := 0
		for i := range cols {
			objs, err := s.RT.Engine.ListObjects(ctx, cols[i].ID)
			if err != nil {
				continue
			}
			n += len(objs)
		}
		counts[kind] = n
	}
	devs, _ := s.RT.IDs.List(ctx)
	active := 0
	for _, d := range devs {
		if !d.Revoked() {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     version.String,
		"head_seq":    head,
		"listen":      s.RT.Cfg.Listen,
		"data_dir":    s.RT.Cfg.DataDir,
		"counts":      counts,
		"devices":     len(devs),
		"devices_active": active,
		"ui_listen":   s.Listen,
		"admin_token": s.RT.Paths.AdminToken,
		"tls_cert":    s.RT.Cfg.TLS.Cert,
	})
}

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.RT.Cfg)
}

func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var cfg struct {
		Listen string `json:"listen"`
		Log    struct {
			Level             string `json:"level"`
			SensitiveMetadata *bool  `json:"sensitive_metadata"`
		} `json:"log"`
		Photos struct {
			StripGPSFromDerivatives *bool `json:"strip_gps_from_derivatives"`
			PerceptualHash          *bool `json:"perceptual_hash"`
			ThumbMaxPx              int   `json:"thumb_max_px"`
			PreviewMaxPx            int   `json:"preview_max_px"`
		} `json:"photos"`
		Sync struct {
			MaxBatch        int   `json:"max_batch"`
			MaxBlobBytes    int64 `json:"max_blob_bytes"`
			TimestampSkewMS int64 `json:"timestamp_skew_ms"`
		} `json:"sync"`
		Pairing struct {
			TTLSeconds       int `json:"ttl_seconds"`
			MaxAttempts      int `json:"max_attempts"`
			MaxActive        int `json:"max_active"`
			BeginsPerHour    int `json:"begins_per_hour"`
			CompletesPerHour int `json:"completes_per_hour"`
		} `json:"pairing"`
		TLS struct {
			Auto                  *bool `json:"auto"`
			AllowInsecureLoopback *bool `json:"allow_insecure_loopback"`
		} `json:"tls"`
		Backup struct {
			Enabled        *bool  `json:"enabled"`
			IntervalHours  int    `json:"interval_hours"`
			PassphraseFile string `json:"passphrase_file"`
			Keep           int    `json:"keep"`
		} `json:"backup"`
	}
	if err := readJSON(r, &cfg); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	c := s.RT.Cfg
	if cfg.Listen != "" {
		c.Listen = cfg.Listen
	}
	if cfg.Log.Level != "" {
		c.Log.Level = cfg.Log.Level
	}
	if cfg.Log.SensitiveMetadata != nil {
		c.Log.SensitiveMetadata = *cfg.Log.SensitiveMetadata
	}
	if cfg.Photos.StripGPSFromDerivatives != nil {
		c.Photos.StripGPSFromDerivatives = *cfg.Photos.StripGPSFromDerivatives
	}
	if cfg.Photos.PerceptualHash != nil {
		c.Photos.PerceptualHash = *cfg.Photos.PerceptualHash
	}
	if cfg.Photos.ThumbMaxPx > 0 {
		c.Photos.ThumbMaxPx = cfg.Photos.ThumbMaxPx
	}
	if cfg.Photos.PreviewMaxPx > 0 {
		c.Photos.PreviewMaxPx = cfg.Photos.PreviewMaxPx
	}
	if cfg.Sync.MaxBatch > 0 {
		c.Sync.MaxBatch = cfg.Sync.MaxBatch
	}
	if cfg.Sync.MaxBlobBytes > 0 {
		c.Sync.MaxBlobBytes = cfg.Sync.MaxBlobBytes
	}
	if cfg.Sync.TimestampSkewMS > 0 {
		c.Sync.TimestampSkewMS = cfg.Sync.TimestampSkewMS
	}
	if cfg.Pairing.TTLSeconds > 0 {
		c.Pairing.TTLSeconds = cfg.Pairing.TTLSeconds
	}
	if cfg.Pairing.MaxAttempts > 0 {
		c.Pairing.MaxAttempts = cfg.Pairing.MaxAttempts
	}
	if cfg.Pairing.MaxActive > 0 {
		c.Pairing.MaxActive = cfg.Pairing.MaxActive
	}
	if cfg.Pairing.BeginsPerHour > 0 {
		c.Pairing.BeginsPerHour = cfg.Pairing.BeginsPerHour
	}
	if cfg.Pairing.CompletesPerHour > 0 {
		c.Pairing.CompletesPerHour = cfg.Pairing.CompletesPerHour
	}
	if cfg.TLS.Auto != nil {
		c.TLS.Auto = *cfg.TLS.Auto
	}
	if cfg.TLS.AllowInsecureLoopback != nil {
		c.TLS.AllowInsecureLoopback = *cfg.TLS.AllowInsecureLoopback
	}
	if cfg.Backup.Enabled != nil {
		c.Backup.Enabled = *cfg.Backup.Enabled
	}
	if cfg.Backup.IntervalHours > 0 {
		c.Backup.IntervalHours = cfg.Backup.IntervalHours
	}
	if cfg.Backup.PassphraseFile != "" {
		c.Backup.PassphraseFile = cfg.Backup.PassphraseFile
	}
	if cfg.Backup.Keep > 0 {
		c.Backup.Keep = cfg.Backup.Keep
	}
	if err := c.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := c.Save(s.RT.Paths.Config); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	s.RT.Cfg = c
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart": "restart nubilo server for listen/TLS/pairing rate changes"})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.RT.IDs.List(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	type row struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Role      string   `json:"role"`
		CreatedAt int64    `json:"created_at"`
		LastSeen  *int64   `json:"last_seen,omitempty"`
		Revoked   bool     `json:"revoked"`
		Protocols []string `json:"protocols,omitempty"`
	}
	out := make([]row, 0, len(list))
	for _, d := range list {
		out = append(out, row{
			ID: d.ID, Name: d.Name, Role: string(d.Role), CreatedAt: d.CreatedAt,
			LastSeen: d.LastSeen, Revoked: d.Revoked(), Protocols: d.Permissions.Protocols,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.RT.IDs.Revoke(r.Context(), req.ID); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": req.ID})
}

func (s *Server) handleDeviceRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.RT.IDs.Rename(r.Context(), req.ID, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "name": req.Name})
}

func (s *Server) handleDevicePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Scope == "" {
		req.Scope = "all"
	}
	dev, pass, err := s.RT.IDs.CreateDAVDevice(r.Context(), req.Name, req.Scope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scheme := "http://"
	if !s.RT.Cfg.Loopback() {
		scheme = "https://"
	}
	base := scheme + s.RT.Cfg.Listen
	urls := map[string]string{}
	if dev.Permissions.HasProtocol("webdav") {
		urls["webdav"] = base + "/dav/"
	}
	if dev.Permissions.HasProtocol("caldav") {
		urls["caldav"] = base + "/caldav/"
	}
	if dev.Permissions.HasProtocol("carddav") {
		urls["carddav"] = base + "/carddav/"
	}
	if dev.Permissions.HasProtocol("photos") {
		urls["photos"] = base + "/api/v1/photos"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": dev.ID,
		"password":  pass,
		"scope":     req.Scope,
		"urls":      urls,
	})
}

func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	cols, err := s.RT.Engine.ChildCollections(r.Context(), kind, "")
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(cols))
	for i := range cols {
		c := &cols[i]
		row := map[string]any{
			"id": c.ID, "kind": c.Kind, "name": c.Name, "revision": c.Revision,
		}
		if kind == "calendar" {
			meta := dav.ParseCalendarColMeta(c.Metadata)
			if meta.Color != "" {
				row["color"] = meta.Color
			}
			if meta.Comp != "" {
				row["comp"] = meta.Comp
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}

func (s *Server) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Kind {
	case "calendar", "addressbook", "files":
	default:
		http.Error(w, "kind must be calendar, addressbook, or files", http.StatusBadRequest)
		return
	}
	c, err := s.RT.Engine.CreateCollection(r.Context(), req.Kind, strings.TrimSpace(req.Name), "", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": c.ID, "kind": c.Kind, "name": c.Name})
}

func (s *Server) handleCollectionRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || id == "" || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.RT.Engine.RenameCollection(r.Context(), id, strings.TrimSpace(req.Name)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": strings.TrimSpace(req.Name)})
}

func (s *Server) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.RT.Engine.DeleteCollection(r.Context(), id); err != nil {
		if err == syncengine.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (s *Server) handleObjectDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.RT.Engine.DeleteObject(r.Context(), id); err != nil {
		if err == syncengine.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (s *Server) handleCollectionUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	col, err := s.RT.Engine.GetCollection(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if col.Kind != "files" {
		http.Error(w, "uploads only for files collections", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.Header.Get("X-Filename"))
	}
	name = filepath.Base(name)
	if !dav.ValidDisplayName(name) {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, s.RT.Cfg.Sync.MaxBlobBytes+1))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	if int64(len(payload)) > s.RT.Cfg.Sync.MaxBlobBytes {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	sum := ncrypto.SHA256Hex(payload)
	blobID, size, err := s.RT.Store.PutBlob(r.Context(), bytes.NewReader(payload), sum)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	meta := dav.EncodeFileMeta(dav.FileMeta{Name: name, MIME: http.DetectContentType(payload)})
	existing, findErr := s.RT.Engine.FindObjectByName(r.Context(), col.ID, name)
	in := syncengine.ChangeInput{
		CollectionID: col.ID,
		Kind:         "file",
		ContentHash:  blobID,
		BlobID:       blobID,
		Size:         size,
		Metadata:     meta,
		Force:        true,
	}
	if errors.Is(findErr, syncengine.ErrNotFound) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else if findErr != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	} else {
		in.ObjectID = existing.ID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = existing.Revision
	}
	res, err := s.RT.Engine.Push(r.Context(), syncengine.LocalOperator(), ids.New(), []syncengine.ChangeInput{in})
	if err != nil || len(res) == 0 || res[0].Status != "ok" {
		http.Error(w, "upload failed", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": in.ObjectID, "name": name, "size": size})
}

func (s *Server) handleCollectionObjects(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	col, err := s.RT.Engine.GetCollection(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	objs, err := s.RT.Engine.ListObjects(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	out := make([]map[string]any, 0, len(objs))
	for i := range objs {
		o := &objs[i]
		row := map[string]any{
			"id": o.ID, "kind": o.Kind, "name": objectLabel(col.Kind, o),
			"size": o.Size, "blob_id": o.BlobID, "updated_at": o.UpdatedAt,
		}
		switch col.Kind {
		case "calendar":
			em := dav.ParseEventMeta(o.Metadata)
			row["uid"] = em.UID
			row["comp"] = em.Comp
			if o.BlobID != "" {
				if pt, err := s.RT.Store.GetBlobPlaintext(o.BlobID); err == nil {
					prev := previewICS(pt)
					row["summary"] = prev.Summary
					row["start_ms"] = prev.Start
					row["end_ms"] = prev.End
					row["all_day"] = prev.AllDay
				}
			}
		case "addressbook":
			cm := dav.ParseContactMeta(o.Metadata)
			row["uid"] = cm.UID
			row["display_name"] = cm.FN
			if cm.FN == "" {
				row["display_name"] = cm.Name
			}
			if cm.Email != "" {
				row["email"] = cm.Email
			}
			if cm.Phone != "" {
				row["phone"] = cm.Phone
			}
			if cm.Birthday != "" {
				row["birthday"] = cm.Birthday
			}
		case "files":
			fm := dav.ParseFileMeta(o.Metadata)
			row["mime"] = fm.MIME
		case "photos":
			pm := photos.ParseMeta(o.Metadata)
			for k, v := range photos.PublicMeta(pm) {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"collection": col, "objects": out})
}

func objectLabel(colKind string, o *syncengine.Object) string {
	switch colKind {
	case "calendar":
		m := dav.ParseEventMeta(o.Metadata)
		if m.Name != "" {
			return m.Name
		}
		return m.UID
	case "addressbook":
		m := dav.ParseContactMeta(o.Metadata)
		if m.FN != "" {
			return m.FN
		}
		if m.Name != "" {
			return m.Name
		}
		return m.UID
	case "files":
		return dav.ParseFileMeta(o.Metadata).Name
	case "photos":
		n := photos.ParseMeta(o.Metadata).Name
		if n != "" {
			return n
		}
	}
	if o.BlobID != "" && len(o.BlobID) >= 12 {
		return o.BlobID[:12]
	}
	return o.ID[:8]
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("hash"))
	if len(hash) != 64 {
		http.Error(w, "bad hash", http.StatusBadRequest)
		return
	}
	pt, err := s.RT.Store.GetBlobPlaintext(hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	mime := r.URL.Query().Get("mime")
	if mime == "" {
		mime = "application/octet-stream"
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		name = hash[:16]
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"")
	}
	w.Header().Set("Content-Type", mime)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pt)
}

func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '"', '\n', '\r', '\x00':
			return '_'
		default:
			return r
		}
	}, name)
	if name == "" {
		return "download"
	}
	return name
}

func (s *Server) photosSvc() photos.Service {
	return photos.Service{
		Engine: s.RT.Engine,
		Store:  s.RT.Store,
		Opt: photos.Options{
			StripGPSFromDerivatives: s.RT.Cfg.Photos.StripGPSFromDerivatives,
			PerceptualHash:          s.RT.Cfg.Photos.PerceptualHash,
			ThumbMaxPx:              s.RT.Cfg.Photos.ThumbMaxPx,
			PreviewMaxPx:            s.RT.Cfg.Photos.PreviewMaxPx,
		},
	}
}

func (s *Server) handlePhotos(w http.ResponseWriter, r *http.Request) {
	objs, err := s.photosSvc().List(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(objs))
	for i := range objs {
		m := photos.ParseMeta(objs[i].Metadata)
		row := photos.PublicMeta(m)
		row["id"] = objs[i].ID
		row["size"] = objs[i].Size
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"photos": out})
}

func (s *Server) handlePhotoRendition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rend := strings.ToLower(r.PathValue("rendition"))
	obj, err := s.photosSvc().Get(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	mime, body, err := s.photosSvc().Blob(obj, rend)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+id+"-"+rend+"\"")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
