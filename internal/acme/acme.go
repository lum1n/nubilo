// Package acme obtains and renews the public certificate (Let's Encrypt by
// default) for the configured domain. TLS-ALPN-01 is answered on the HTTPS
// listener and works through the raw TCP relay; HTTP-01 is answered on :80
// as a fallback.
package acme

import (
	"os"
	"strings"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"nubilo/internal/config"
)

// Manager returns an autocert manager for cfg, or nil when ACME is disabled.
// Account keys and certificates are cached in cacheDir (0700).
func Manager(cfg config.PublicConfig, cacheDir string) (*autocert.Manager, error) {
	if !cfg.ACME.Enabled || strings.TrimSpace(cfg.Domain) == "" {
		return nil, nil
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(cfg.Domain),
		Email:      cfg.ACME.Email,
	}
	if cfg.ACME.Directory != "" {
		m.Client = &acme.Client{DirectoryURL: cfg.ACME.Directory}
	}
	return m, nil
}

// ALPNProto is the TLS-ALPN-01 protocol name; it must be offered in NextProtos.
const ALPNProto = acme.ALPNProto
