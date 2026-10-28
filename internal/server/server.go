package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"nubilo/internal/audit"
	"nubilo/internal/auth"
	"nubilo/internal/authz"
	"nubilo/internal/config"
	"nubilo/internal/dav"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
	"nubilo/internal/wiredx"

	"github.com/emersion/go-webdav"
)

type Server struct {
	Cfg       config.Config
	Store     *store.Store
	IDs       *identity.Service
	Auth      *auth.Authenticator
	Engine    *syncengine.Engine
	Audit     *audit.Logger
	Log       *slog.Logger
	ServerID  string
	ServerPub ed25519.PublicKey

	http      *http.Server
	pairLim   *ipLimiter
	pairOK    *ipLimiter
	sigFail   *ipLimiter
	redeemLim *ipLimiter
	// webLoginLim limits browser sign-in starts per IP.
	webLoginLim *ipLimiter
	davAuth     *dav.Auth

	mu    sync.Mutex
	extra []*http.Server // public listeners (direct, relay)
	node  wiredx.Node
}

func New(cfg config.Config, st *store.Store, ids *identity.Service, a *auth.Authenticator, eng *syncengine.Engine, al *audit.Logger, log *slog.Logger, serverPub ed25519.PublicKey) *Server {
	s := &Server{
		Cfg:         cfg,
		Store:       st,
		IDs:         ids,
		Auth:        a,
		Engine:      eng,
		Audit:       al,
		Log:         log,
		ServerPub:   serverPub,
		pairLim:     newIPLimiter(cfg.Pairing.BeginsPerHour, time.Hour),
		pairOK:      newIPLimiter(cfg.Pairing.CompletesPerHour, time.Hour),
		sigFail:     newIPLimiter(30, time.Minute),
		redeemLim:   newIPLimiter(5, time.Hour),
		webLoginLim: newIPLimiter(30, time.Hour),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pair/begin", s.handlePairBegin)
	mux.HandleFunc("POST /api/v1/pair/complete", s.handlePairComplete)
	mux.HandleFunc("POST /api/v1/invites/redeem", s.handleInviteRedeem)
	mux.HandleFunc("GET /api/v1/profile/{token}", s.handleProfileDownload)
	mux.HandleFunc("GET /join", s.handleJoinPage)
	mux.HandleFunc("GET /{$}", s.handleHomePage)
	s.webRoutes(mux)
	mux.HandleFunc("GET /api/v1/status", s.authed(s.handleStatus))
	mux.HandleFunc("GET /api/v1/me", s.authed(s.handleMe))
	mux.HandleFunc("GET /api/v1/me/passwords", s.authed(s.handleMyPasswords))
	mux.HandleFunc("POST /api/v1/me/passwords", s.authed(s.handleCreatePassword))
	mux.HandleFunc("DELETE /api/v1/me/passwords/{id}", s.authed(s.handleRevokePassword))
	mux.HandleFunc("POST /api/v1/me/profile", s.authed(s.handleCreateProfile))
	mux.HandleFunc("POST /api/v1/me/upload-token", s.authed(s.handleUploadToken))
	mux.HandleFunc("POST /api/v1/me/device-invite", s.authed(s.handleDeviceInvite))
	mux.HandleFunc("GET /api/v1/users", s.authed(s.handleUserDirectory))
	mux.HandleFunc("GET /api/v1/groups", s.authed(s.handleGroups))
	mux.HandleFunc("POST /api/v1/admin/groups", s.authed(s.handleAdminCreateGroup))
	mux.HandleFunc("PATCH /api/v1/admin/groups/{id}", s.authed(s.handleAdminPatchGroup))
	mux.HandleFunc("DELETE /api/v1/admin/groups/{id}", s.authed(s.handleAdminDeleteGroup))
	mux.HandleFunc("GET /api/v1/collections", s.authed(s.handleCollectionList))
	mux.HandleFunc("GET /api/v1/collections/{id}/shares", s.authed(s.handleSharesGet))
	mux.HandleFunc("PUT /api/v1/collections/{id}/shares", s.authed(s.handleSharesPut))
	mux.HandleFunc("DELETE /api/v1/collections/{id}/shares", s.authed(s.handleSharesDelete))
	mux.HandleFunc("GET /api/v1/admin/users", s.authed(s.handleAdminUsers))
	mux.HandleFunc("POST /api/v1/admin/users", s.authed(s.handleAdminCreateUser))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", s.authed(s.handleAdminPatchUser))
	mux.HandleFunc("GET /api/v1/admin/invites", s.authed(s.handleAdminInvites))
	mux.HandleFunc("POST /api/v1/admin/invites", s.authed(s.handleAdminCreateInvite))
	mux.HandleFunc("DELETE /api/v1/admin/invites/{id}", s.authed(s.handleAdminRevokeInvite))
	mux.HandleFunc("GET /api/v1/admin/stats", s.authed(s.handleAdminStats))
	mux.HandleFunc("POST /api/v1/admin/devices/{id}/revoke", s.authed(s.handleAdminRevokeDevice))
	mux.HandleFunc("GET /api/v1/devices", s.authed(s.handleDevices))
	mux.HandleFunc("POST /api/v1/devices/{id}/revoke", s.authed(s.handleRevoke))
	mux.HandleFunc("POST /api/v1/devices/{id}/rename", s.authed(s.handleRename))
	mux.HandleFunc("POST /sync/v1/hello", s.authed(s.handleHello))
	mux.HandleFunc("POST /sync/v1/collections", s.authed(s.handleCollections))
	mux.HandleFunc("POST /sync/v1/changes", s.authed(s.handleChanges))
	mux.HandleFunc("POST /sync/v1/push", s.authed(s.handlePush))
	mux.HandleFunc("POST /sync/v1/ack", s.authed(s.handleAck))
	mux.HandleFunc("POST /sync/v1/reconcile", s.authed(s.handleReconcile))
	mux.HandleFunc("POST /sync/v1/collection", s.authed(s.handleEnsureCollection))
	mux.HandleFunc("POST /sync/v1/objects", s.authed(s.handleObjects))
	mux.HandleFunc("POST /sync/v1/object", s.authed(s.handleObject))
	mux.HandleFunc("POST /sync/v1/collection/update", s.authed(s.handleCollectionUpdate))
	mux.HandleFunc("POST /sync/v1/collection/delete", s.authed(s.handleCollectionDelete))
	mux.HandleFunc("POST /sync/v1/thumb", s.authed(s.handleThumb))
	mux.HandleFunc("PUT /sync/v1/blob/{hash}", s.authedUpload(s.handleBlobPut))
	mux.HandleFunc("GET /sync/v1/blob/{hash}", s.authed(s.handleBlobGet))
	mux.HandleFunc("GET /api/v1/photos", s.authedAny(s.handlePhotosList))
	mux.HandleFunc("POST /api/v1/photos", s.authedAny(s.handlePhotosUpload))
	mux.HandleFunc("GET /api/v1/photos/{id}", s.authedAny(s.handlePhotoMeta))
	mux.HandleFunc("GET /api/v1/photos/{id}/original", s.authedAny(s.handlePhotoOriginal))
	mux.HandleFunc("GET /api/v1/photos/{id}/preview", s.authedAny(s.handlePhotoPreview))
	mux.HandleFunc("GET /api/v1/photos/{id}/thumb", s.authedAny(s.handlePhotoThumb))
	mux.HandleFunc("POST /api/v1/photos/check", s.authedUpload(s.handlePhotosCheck))
	mux.HandleFunc("POST /api/v1/photos/commit", s.authedUpload(s.handlePhotosCommit))
	mux.HandleFunc("GET /api/v1/albums", s.authedAny(s.handleAlbums))
	mux.HandleFunc("POST /api/v1/albums", s.authedAny(s.handleCreateAlbum))
	mux.HandleFunc("GET /api/v1/albums/{id}/items", s.authedAny(s.handleAlbumItems))
	mux.HandleFunc("POST /api/v1/albums/{id}/items", s.authedAny(s.handleAddAlbumItems))
	davAuth := dav.NewAuth(ids)
	s.davAuth = davAuth
	fs := dav.NewFS(eng, st)
	fs.Photos = s.photos
	davH := davAuth.Middleware(dav.LockCompat(&webdav.Handler{FileSystem: fs}))
	calH := davAuth.Middleware(dav.WrapCalDAV(dav.NewCalDAV(eng, st)))
	cardH := davAuth.Middleware(dav.WrapCardDAV(dav.NewCardDAV(eng, st)))
	mux.Handle("/dav/", davH)
	mux.Handle("/dav", davH)
	mux.Handle("/caldav/", calH)
	mux.Handle("/caldav", calH)
	mux.Handle("/carddav/", cardH)
	mux.Handle("/carddav", cardH)
	// Discovery redirects to the authenticated user's principal, so it runs
	// inside the auth middleware.
	calWK := davAuth.Middleware(dav.WellKnown(dav.CalDAVPrefix, eng, calH))
	cardWK := davAuth.Middleware(dav.WellKnown(dav.CardDAVPrefix, eng, cardH))
	mux.Handle("/.well-known/caldav", calWK)
	mux.Handle("/.well-known/caldav/", calWK)
	mux.Handle("/.well-known/carddav", cardWK)
	mux.Handle("/.well-known/carddav/", cardWK)
	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.limitBody(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    cfg.HTTP.MaxHeaderBytes,
	}
	return s
}

func (s *Server) limitBody(next http.Handler) http.Handler {
	max := s.Cfg.Sync.MaxBlobBytes + 1<<20
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, max)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// ListenAndServe serves the LAN listener only. Run serves every configured listener.
func (s *Server) ListenAndServe() error {
	return s.Run(context.Background())
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	servers := append([]*http.Server{s.http}, s.extra...)
	node := s.node
	s.mu.Unlock()
	var first error
	for _, h := range servers {
		if err := h.Shutdown(ctx); err != nil && first == nil {
			first = err
		}
	}
	if node != nil {
		_ = node.Close()
	}
	return first
}

type ctxKey int

const deviceKey ctxKey = 1

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, err := s.Auth.AuthenticateRequest(r)
		if err != nil {
			s.rejectAuth(w, r, err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), deviceKey, dev)))
	}
}

func (s *Server) rejectAuth(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrBodyTooLarge) {
		s.Log.Info("auth_failed", "err", err.Error(), "path", r.URL.Path)
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if errors.Is(err, auth.ErrBodyRead) {
		s.Log.Info("auth_failed", "err", err.Error(), "path", r.URL.Path)
		http.Error(w, "incomplete body", http.StatusBadRequest)
		return
	}
	// Only count intentional auth abuse toward the ban window. Truncated uploads
	// (client timeout) and similar read errors must not cascade into 429s.
	if auth.CountsTowardFailLimit(err) {
		if s.sigFail != nil && !s.sigFail.Allow(clientIP(r)) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
	}
	s.Log.Info("auth_failed", "err", err.Error(), "path", r.URL.Path)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func deviceFrom(r *http.Request) *identity.Device {
	d, _ := r.Context().Value(deviceKey).(*identity.Device)
	return d
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) handlePairBegin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.pairLim.Allow(ip) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
		Nonce     string `json:"nonce"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pub, err := decodeKey(req.PublicKey)
	if err != nil {
		http.Error(w, "bad public key", http.StatusBadRequest)
		return
	}
	res, err := s.IDs.Begin(r.Context(), identity.BeginRequest{
		Code: req.Code, Name: req.Name, PublicKey: pub,
	})
	if err != nil {
		s.Audit.Event(r.Context(), "", "pair.begin_fail", map[string]any{"class": err.Error()})
		status := http.StatusUnauthorized
		if errors.Is(err, identity.ErrTooManyActive) || errors.Is(err, identity.ErrTooManyTries) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "pairing failed", status)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"pairing_id": res.PairingID,
		"challenge":  b64(res.Challenge),
	})
}

func (s *Server) handlePairComplete(w http.ResponseWriter, r *http.Request) {
	if s.pairOK != nil && !s.pairOK.Allow(clientIP(r)) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	var req struct {
		PairingID string `json:"pairing_id"`
		Signature string `json:"signature"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sig, err := decodeKey(req.Signature)
	if err != nil || !ids.Valid(req.PairingID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dev, err := s.IDs.Complete(r.Context(), req.PairingID, sig)
	if err != nil {
		s.Audit.Event(r.Context(), "", "pair.complete_fail", map[string]any{"class": err.Error()})
		http.Error(w, "pairing failed", http.StatusUnauthorized)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "pair.complete", map[string]any{"name": "(redacted)"})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"device_id":         dev.ID,
		"user_id":           dev.UserID,
		"created_at":        dev.CreatedAt,
		"server_public_key": b64(s.ServerPub),
	})
}

// APIVersion is bumped when the HTTP API gains incompatible changes.
const APIVersion = 2

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	head, _ := s.Engine.HeadSeq(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"head_seq":    head,
		"listen":      s.Cfg.Listen,
		"api_version": APIVersion,
		"server_name": s.Cfg.Public.Name(),
		"domain":      s.Cfg.Public.Domain,
		"features":    []string{"users", "shares", "invites", "profiles", "upload_tokens", "albums"},
		"relay":       s.relayStatus(),
	})
}

// relayStatus reports the wired relay connection for doctor (nil when unused).
func (s *Server) relayStatus() map[string]any {
	if s.Cfg.Public.Relay.Invite == "" {
		return nil
	}
	s.mu.Lock()
	node := s.node
	s.mu.Unlock()
	if node == nil {
		return map[string]any{"joined": false, "path": "none"}
	}
	path := "none"
	for _, m := range node.Members() {
		if m.Anchor {
			if p := node.Path(m.Name); p != "none" {
				path = p
			}
		}
	}
	return map[string]any{"joined": true, "node": node.Name(), "path": path}
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.DeviceList); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var list []identity.Device
	var err error
	if dev.System() || (r.URL.Query().Get("all") == "1" && dev.IsAdmin()) {
		list, err = s.IDs.List(r.Context())
	} else {
		list, err = s.IDs.ListUserDevices(r.Context(), dev.UserID)
	}
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]deviceRow, 0, len(list))
	for _, d := range list {
		out = append(out, toDeviceRow(&d))
	}
	s.writeJSON(w, http.StatusOK, out)
}

type deviceRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	UserID    string `json:"user_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  *int64 `json:"last_seen,omitempty"`
	Revoked   bool   `json:"revoked"`
}

func toDeviceRow(d *identity.Device) deviceRow {
	return deviceRow{ID: d.ID, Name: d.Name, Role: string(d.Role), UserID: d.UserID, CreatedAt: d.CreatedAt, LastSeen: d.LastSeen, Revoked: d.Revoked()}
}

// ownsDevice reports whether caller may manage target: same user, or admin.
func (s *Server) ownsDevice(r *http.Request, caller *identity.Device, targetID string) bool {
	if caller.IsAdmin() {
		return true
	}
	t, err := s.IDs.Get(r.Context(), targetID)
	return err == nil && caller.UserID != "" && t.UserID == caller.UserID
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	id := r.PathValue("id")
	if dev.Revoked() || !s.ownsDevice(r, dev, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.IDs.Revoke(r.Context(), id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "device.revoke", map[string]any{"target": id})
	s.writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	id := r.PathValue("id")
	if dev.Revoked() || !s.ownsDevice(r, dev, id) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.IDs.Rename(r.Context(), id, req.Name); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": req.Name})
}

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ProtocolMin int   `json:"protocol_min"`
		ProtocolMax int   `json:"protocol_max"`
		Cursor      int64 `json:"cursor"`
		RestoreHint bool  `json:"restore_hint"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h, err := s.Engine.Hello(r.Context(), syncengine.ScopeOf(dev), req.Cursor, req.RestoreHint)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cols, err := s.Engine.GetCollections(r.Context(), syncengine.ScopeOf(dev))
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"collections": cols})
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		SinceSeq     int64  `json:"since_seq"`
		Limit        int    `json:"limit"`
		CollectionID string `json:"collection_id"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.CollectionID != "" {
		if access, err := s.Engine.AccessFor(r.Context(), dev, req.CollectionID); err != nil || access < authz.AccessRead {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	res, err := s.Engine.GetChanges(r.Context(), syncengine.ScopeOf(dev), req.SinceSeq, req.Limit, req.CollectionID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IdempotencyKey string                   `json:"idempotency_key"`
		Changes        []syncengine.ChangeInput `json:"changes"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	res, err := s.Engine.Push(r.Context(), dev, req.IdempotencyKey, req.Changes)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Seq int64 `json:"seq"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.Engine.Ack(r.Context(), dev.ID, req.Seq); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"acked": req.Seq})
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		CollectionID string                     `json:"collection_id"`
		Objects      []syncengine.InventoryItem `json:"objects"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	res, err := s.Engine.Reconcile(r.Context(), dev, req.CollectionID, req.Objects)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEnsureCollection(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Kind     string          `json:"kind"`
		Name     string          `json:"name"`
		ParentID string          `json:"parent_id"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ParentID != "" {
		if access, err := s.Engine.AccessFor(r.Context(), dev, req.ParentID); err != nil || access < authz.AccessWrite {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	c, err := s.Engine.EnsureChildCollection(r.Context(), dev.UserID, req.Kind, req.ParentID, req.Name)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(req.Metadata) > 0 && string(req.Metadata) != "null" {
		if access, err := s.Engine.AccessFor(r.Context(), dev, c.ID); err != nil || access < authz.AccessOwner {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		c, err = s.Engine.PatchCollectionMetadata(r.Context(), c.ID, syncengine.MirrorPatch(req.Metadata, dev))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	s.writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleBlobPut(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hash := strings.ToLower(r.PathValue("hash"))
	if len(hash) != 64 {
		http.Error(w, "bad hash", http.StatusBadRequest)
		return
	}
	sum, size, err := s.Store.PutUserBlob(r.Context(), dev.UserID, r.Body, hash)
	if err != nil {
		http.Error(w, "blob rejected", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"blob_id": sum, "size": size})
}

func (s *Server) handleBlobGet(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hash := strings.ToLower(r.PathValue("hash"))
	if !dev.System() {
		// Blobs are deduplicated across users; never confirm that a hash
		// exists unless the caller already has a right to it.
		ok, err := s.Engine.BlobReadable(r.Context(), dev.UserID, hash)
		if err != nil || !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	pt, err := s.Store.GetBlobPlaintext(hash)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pt)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func decodeKey(s string) ([]byte, error) {
	return b64decode(s)
}
