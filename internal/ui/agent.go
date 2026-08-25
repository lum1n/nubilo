package ui

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nubilo/internal/config"
)

const DefaultAgentListen = "127.0.0.1:8788"

// AgentServer is the Mac agent configuration UI (selection + setup).
type AgentServer struct {
	DataDir string
	SelPath string
	Log     *slog.Logger
	Listen  string
	gate    sessionGate
	http    *http.Server
	mu      sync.Mutex
}

// NewAgent creates a loopback agent UI bound to dataDir's agent.json / device.json.
func NewAgent(dataDir, listen string, log *slog.Logger) (*AgentServer, error) {
	if listen == "" {
		listen = DefaultAgentListen
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
	paths := config.Paths(dataDir)
	s := &AgentServer{
		DataDir: dataDir,
		SelPath: paths.AgentJSON,
		Log:     log,
		Listen:  listen,
		gate:    gate,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/overview", s.gate.authed(s.handleOverview))
	mux.HandleFunc("GET /api/health", s.gate.authed(s.handleAgentHealth))
	mux.HandleFunc("GET /api/selection", s.gate.authed(s.handleSelectionGet))
	mux.HandleFunc("PUT /api/selection", s.gate.authed(s.handleSelectionPut))
	mux.HandleFunc("GET /api/calendars", s.gate.authed(s.handleCalendars))
	mux.HandleFunc("POST /api/calendars/select", s.gate.authed(s.handleCalendarSelect))
	mux.HandleFunc("POST /api/calendars/unselect", s.gate.authed(s.handleCalendarUnselect))
	mux.HandleFunc("GET /api/reminders", s.gate.authed(s.handleReminders))
	mux.HandleFunc("POST /api/reminders/select", s.gate.authed(s.handleReminderSelect))
	mux.HandleFunc("POST /api/reminders/unselect", s.gate.authed(s.handleReminderUnselect))
	mux.HandleFunc("GET /api/albums", s.gate.authed(s.handleAlbums))
	mux.HandleFunc("POST /api/albums/select", s.gate.authed(s.handleAlbumSelect))
	mux.HandleFunc("POST /api/albums/unselect", s.gate.authed(s.handleAlbumUnselect))
	mux.HandleFunc("POST /api/photos/authorize", s.gate.authed(s.handlePhotosAuthorize))
	mux.HandleFunc("POST /api/files/add", s.gate.authed(s.handleFilesAdd))
	mux.HandleFunc("POST /api/files/remove", s.gate.authed(s.handleFilesRemove))
	mux.HandleFunc("POST /api/pair", s.gate.authed(s.handlePair))
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

func (s *AgentServer) SessionURL() string {
	return s.gate.SessionURL(s.Listen)
}

func (s *AgentServer) Handler() http.Handler {
	return s.http.Handler
}

func (s *AgentServer) ListenAndServe() error {
	s.Log.Info("agent_ui_listen", "addr", s.Listen, "url", s.SessionURL(), "data_dir", s.DataDir)
	return s.http.ListenAndServe()
}

func (s *AgentServer) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *AgentServer) serveIndex(w http.ResponseWriter, r *http.Request) {
	if tok := r.URL.Query().Get("session"); tok != "" {
		if s.gate.setSessionCookie(w, tok) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	serveStatic(w, "agent.html")
}

func (s *AgentServer) serveSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/assets/") {
		http.NotFound(w, r)
		return
	}
	serveStatic(w, "agent.html")
}

func (s *AgentServer) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.gate.sessionOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func absPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
