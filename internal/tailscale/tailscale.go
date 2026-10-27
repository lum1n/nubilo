// Package tailscale gets this host's MagicDNS name and its certificate from
// the local tailscaled, through the `tailscale` CLI.
//
// `tailscale cert` returns a Let's Encrypt certificate for the name, so
// iPhones and Macs on the tailnet trust the server without installing
// anything. tailscaled caches the certificate and renews it when it is
// called near expiry; Certs calls it once a day.
package tailscale

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Run executes the tailscale CLI and returns its stdout. Tests replace it.
var Run = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "tailscale", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("tailscale %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("tailscale %s: %w", args[0], err)
	}
	return out, nil
}

// Self describes this node as tailscaled reports it.
type Self struct {
	// Name is the MagicDNS name without the trailing dot.
	Name string
	// CertOK reports whether the tailnet can issue a certificate for Name
	// (MagicDNS and HTTPS certificates are on in the admin console).
	CertOK bool
	// Online is false when tailscaled is logged out or stopped.
	Online bool
}

// Status reads `tailscale status --json`.
func Status(ctx context.Context) (Self, error) {
	out, err := Run(ctx, "status", "--json")
	if err != nil {
		return Self{}, err
	}
	var st struct {
		BackendState string
		CertDomains  []string
		Self         struct {
			DNSName string
			Online  bool
		}
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return Self{}, fmt.Errorf("tailscale status: %w", err)
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" {
		return Self{}, errors.New("tailscale: this node has no MagicDNS name (turn on MagicDNS in the admin console)")
	}
	return Self{
		Name:   name,
		CertOK: slices.Contains(st.CertDomains, name),
		Online: st.BackendState == "Running",
	}, nil
}

// Certs keeps the certificate for one MagicDNS name, stored as
// <Dir>/<Name>.crt and .key.
type Certs struct {
	Name string
	Dir  string
	Log  *slog.Logger

	mu   sync.RWMutex
	cert *tls.Certificate
}

// Refresh asks tailscaled for the certificate (it renews when due) and loads it.
func (c *Certs) Refresh(ctx context.Context) error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	crt := filepath.Join(c.Dir, c.Name+".crt")
	key := filepath.Join(c.Dir, c.Name+".key")
	if _, err := Run(ctx, "cert", "--cert-file", crt, "--key-file", key, c.Name); err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		return fmt.Errorf("tailscale cert: %w", err)
	}
	c.mu.Lock()
	c.cert = &pair
	c.mu.Unlock()
	return nil
}

// Get returns the certificate when sni is the MagicDNS name and one is loaded.
func (c *Certs) Get(sni string) *tls.Certificate {
	if c == nil || !strings.EqualFold(strings.TrimSuffix(sni, "."), c.Name) {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cert
}

// Loaded reports whether a certificate is available.
func (c *Certs) Loaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cert != nil
}

// Keep refreshes the certificate every interval until ctx ends. Failures
// keep the previous certificate.
func (c *Certs) Keep(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Refresh(ctx); err != nil && c.Log != nil {
				c.Log.Warn("tailscale_cert", "name", c.Name, "err", err.Error())
			}
		}
	}
}
