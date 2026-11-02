package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"nubilo/internal/update"
	"nubilo/internal/wiredx"
)

// ServeAdmin answers update requests from overlay members (the home
// server) on wiredx.ServiceAdmin until ctx ends. A member can only ask for
// the newest signed release, which the relay fetches and checks itself.
func ServeAdmin(ctx context.Context, node wiredx.Node, m *update.Manager, log *slog.Logger) error {
	ln, err := node.Listen(wiredx.ServiceAdmin)
	if err != nil {
		return err
	}
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /update", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, update.RemoteStatus{Status: m.Status(), WiredProtocol: wiredx.Protocol})
	})
	mux.HandleFunc("POST /update/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Version == "" {
			reply(w, http.StatusBadRequest, map[string]string{"error": "version required"})
			return
		}
		cctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		st, err := m.Check(cctx)
		cancel()
		if err != nil {
			reply(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		// Only what this relay itself sees as the newest signed release.
		if st.Available == nil || st.Available.Version != req.Version {
			newest := st.Current
			if st.Available != nil {
				newest = st.Available.Version
			}
			reply(w, http.StatusConflict, map[string]string{"error": "the relay sees " + newest + " as the newest release"})
			return
		}
		st, err = m.StartInstall()
		if err != nil && !errors.Is(err, update.ErrBusy) {
			reply(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		log.Info("relay_update_requested", "to", req.Version)
		reply(w, http.StatusAccepted, update.RemoteStatus{Status: st, WiredProtocol: wiredx.Protocol})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("relay_admin", "err", err.Error())
		}
	}()
	return nil
}

// Reconnected reports whether a member other than the anchor itself (the
// home server) has a path to it: what a freshly updated relay must reach
// in time, or roll back.
func Reconnected(node wiredx.Node) bool {
	for _, m := range node.Members() {
		if !m.Anchor && node.Path(m.Name) != "none" {
			return true
		}
	}
	return false
}
