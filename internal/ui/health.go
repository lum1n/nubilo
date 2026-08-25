package ui

import (
	"net/http"

	"nubilo/internal/config"
	"nubilo/internal/doctor"
	"nubilo/internal/setup"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	rep, err := doctor.Server(r.Context(), s.RT.Cfg.DataDir, doctor.Options{Verify: r.URL.Query().Get("verify") == "1"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleSetupBackup(w http.ResponseWriter, r *http.Request) {
	pass, passFile, err := setup.EnableAutoBackup(s.RT.Cfg.DataDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if cfg, err := config.Load(s.RT.Paths.Config); err == nil {
		s.RT.Cfg = cfg
	}
	out := map[string]any{
		"ok":              true,
		"passphrase_file": passFile,
	}
	if pass != "" {
		out["passphrase"] = pass
		out["note"] = "save the passphrase offline — it is only shown once"
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *AgentServer) handleAgentHealth(w http.ResponseWriter, r *http.Request) {
	rep, err := doctor.Agent(s.DataDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
