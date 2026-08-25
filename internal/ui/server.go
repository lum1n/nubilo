package ui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"nubilo/internal/app"
)

const DefaultListen = "127.0.0.1:8787"

type Server struct {
	RT     *app.Runtime
	Log    *slog.Logger
	Listen string
	gate   sessionGate
	http   *http.Server

	backupMu  sync.Mutex
	backupTok map[string]dlToken
}

func New(rt *app.Runtime, listen string, log *slog.Logger) (*Server, error) {
	if listen == "" {
		listen = DefaultListen
	}
	listen, err := validateLoopbackListen(listen)
	if err != nil {
		return nil, err
	}
	gate, err := newSessionGate()
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Server{RT: rt, Log: log, Listen: listen, gate: gate}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("GET /api/overview", s.authed(s.handleOverview))
	mux.HandleFunc("GET /api/status", s.authed(s.handleStatus))
	mux.HandleFunc("GET /api/config", s.authed(s.handleConfigGet))
	mux.HandleFunc("PUT /api/config", s.authed(s.handleConfigPut))
	mux.HandleFunc("GET /api/devices", s.authed(s.handleDevices))
	mux.HandleFunc("POST /api/devices/revoke", s.authed(s.handleDeviceRevoke))
	mux.HandleFunc("POST /api/devices/rename", s.authed(s.handleDeviceRename))
	mux.HandleFunc("POST /api/devices/password", s.authed(s.handleDevicePassword))
	mux.HandleFunc("GET /api/collections", s.authed(s.handleCollections))
	mux.HandleFunc("POST /api/collections", s.authed(s.handleCollectionCreate))
	mux.HandleFunc("POST /api/collections/{id}/rename", s.authed(s.handleCollectionRename))
	mux.HandleFunc("DELETE /api/collections/{id}", s.authed(s.handleCollectionDelete))
	mux.HandleFunc("GET /api/collections/{id}/objects", s.authed(s.handleCollectionObjects))
	mux.HandleFunc("POST /api/collections/{id}/upload", s.authed(s.handleCollectionUpload))
	mux.HandleFunc("DELETE /api/objects/{id}", s.authed(s.handleObjectDelete))
	mux.HandleFunc("GET /api/blobs/{hash}", s.authed(s.handleBlob))
	mux.HandleFunc("GET /api/photos", s.authed(s.handlePhotos))
	mux.HandleFunc("GET /api/photos/{id}/{rendition}", s.authed(s.handlePhotoRendition))
	mux.HandleFunc("POST /api/pair", s.authed(s.handlePairStart))
	mux.HandleFunc("GET /api/pair/{id}", s.authed(s.handlePairStatus))
	mux.HandleFunc("POST /api/verify", s.authed(s.handleVerify))
	mux.HandleFunc("POST /api/gc", s.authed(s.handleGC))
	mux.HandleFunc("POST /api/backup", s.authed(s.handleBackupCreate))
	mux.HandleFunc("GET /api/backup/download/{token}", s.authed(s.handleBackupDownload))
	mux.HandleFunc("POST /api/restore", s.authed(s.handleRestore))
	mux.HandleFunc("POST /api/tls", s.authed(s.handleTLSRegen))
	mux.HandleFunc("POST /api/devices/enroll", s.authed(s.handleDeviceEnroll))
	mux.HandleFunc("POST /api/devices/rotate", s.authed(s.handleDeviceRotate))
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /{$}", s.serveIndex)
	mux.HandleFunc("GET /", s.serveSPA)
	s.http = &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

func (s *Server) SessionURL() string {
	return s.gate.SessionURL(s.Listen)
}

func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

func (s *Server) ListenAndServe() error {
	s.Log.Info("ui_listen", "addr", s.Listen, "url", s.SessionURL())
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if tok := r.URL.Query().Get("session"); tok != "" {
		if s.gate.setSessionCookie(w, tok) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	serveStatic(w, "index.html")
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/assets/") {
		http.NotFound(w, r)
		return
	}
	serveStatic(w, "index.html")
}

func serveStatic(w http.ResponseWriter, name string) {
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	switch name {
	case "index.html", "agent.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case "app.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "app.js", "agent.js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	b, err := fs.ReadFile(static, name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) checkAuth(r *http.Request) bool {
	if s.gate.sessionOK(r) {
		return true
	}
	return s.matchAdmin(r)
}

func (s *Server) matchAdmin(r *http.Request) bool {
	tok := strings.TrimSpace(r.Header.Get("X-Nubilo-Admin"))
	if tok == "" {
		return false
	}
	return s.matchAdminToken(tok)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isLoopbackHost(host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"admin_token": s.RT.Paths.AdminToken,
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.matchAdminToken(req.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.gate.setSessionCookie(w, s.gate.sessionHex())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) matchAdminToken(tok string) bool {
	if len(s.RT.AdminTok) == 0 {
		return false
	}
	sumTok := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	sumWant := sha256.Sum256(s.RT.AdminTok)
	return subtle.ConstantTimeCompare(sumTok[:], sumWant[:]) == 1
}
