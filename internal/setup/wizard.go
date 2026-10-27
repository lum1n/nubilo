package setup

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"nubilo/internal/app"
	"nubilo/internal/config"
	"nubilo/internal/identity"
	"nubilo/internal/qr"
	"nubilo/internal/server"
	"nubilo/internal/tailscale"
	"nubilo/internal/wiredx"
)

// Reachability modes: how phones outside the home reach the server.
const (
	ModeRelay  = "relay"  // a VPS runs `nubilo relay`; the home stays closed
	ModeDirect = "direct" // this host is public (port forward or VPS)
	ModeLAN    = "lan"    // LAN only, self-signed certificate
	// ModeTailscale: devices on the tailnet use the MagicDNS name with a
	// certificate from `tailscale cert`; TLS ends at home.
	ModeTailscale = "tailscale"
	// ModeProxy: `nubilo relay --behind-proxy` runs on a host whose
	// TLS-terminating proxy (exe.dev) owns the certificate; that proxy can
	// read the traffic. The home stays closed, as in relay mode.
	ModeProxy = "proxy"
)

// Wizard holds the answers of `nubilo setup`. Each step is idempotent so the
// Mac app can drive them one at a time (`nubilo setup --json --step N`).
type Wizard struct {
	DataDir     string
	Listen      string
	Mode        string
	Domain      string
	Email       string
	RelayInvite string
	// Staging uses the Let's Encrypt staging CA (for dry runs).
	Staging bool
	// VerifyTimeout bounds how long StepVerify waits for the public URL.
	VerifyTimeout time.Duration
}

// StepResult is one wizard step outcome; it is also the --json line format.
type StepResult struct {
	Step    int            `json:"step"`
	Name    string         `json:"name"`
	OK      bool           `json:"ok"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// Step names in order; StepNames[i] is step i+1.
var StepNames = []string{"init", "reachability", "service", "verify", "invite"}

func fail(step int, err error) StepResult {
	return StepResult{Step: step, Name: StepNames[step-1], Error: err.Error()}
}

// StepInit creates the data dir, keys and config if missing.
func (w *Wizard) StepInit() StepResult {
	created, err := EnsureServerInitialized(w.DataDir, w.Listen)
	if err != nil {
		return fail(1, err)
	}
	msg := "already initialized"
	if created {
		msg = "initialized " + w.DataDir
	}
	return StepResult{Step: 1, Name: "init", OK: true, Message: msg, Data: map[string]any{"created": created}}
}

// StepReachability writes the public config for the chosen mode. For relay
// mode it joins the overlay once to validate the invite.
func (w *Wizard) StepReachability(ctx context.Context) StepResult {
	p := config.Paths(w.DataDir)
	cfg, err := config.Load(p.Config)
	if err != nil {
		return fail(2, err)
	}
	cfg.DataDir = w.DataDir
	w.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(w.Domain), "."))
	data := map[string]any{"mode": w.Mode}
	cfg.Public.Tailscale = config.TailscaleConfig{}
	switch w.Mode {
	case ModeLAN:
		cfg.Public.Domain = ""
		cfg.Public.ACME = config.ACMEConfig{}
		cfg.Public.Relay = config.RelayConfig{}
		cfg.Public.DirectListen = ""
	case ModeTailscale:
		if cfg.Loopback() {
			return fail(2, fmt.Errorf("listen %s is loopback only; tailnet devices need e.g. --listen 0.0.0.0:8443", cfg.Listen))
		}
		self, err := tailscale.Status(ctx)
		if err != nil {
			return fail(2, err)
		}
		if !self.CertOK {
			return fail(2, fmt.Errorf("the tailnet issues no certificate for %s: turn on MagicDNS and HTTPS Certificates in the Tailscale admin console (DNS page)", self.Name))
		}
		cfg.Public.Domain = ""
		cfg.Public.ACME = config.ACMEConfig{}
		cfg.Public.Relay = config.RelayConfig{}
		cfg.Public.DirectListen = ""
		cfg.Public.Tailscale = config.TailscaleConfig{Enabled: true, Name: self.Name}
		data["name"] = self.Name
		certs := &tailscale.Certs{Name: self.Name, Dir: p.Tailscale}
		if err := certs.Refresh(ctx); err != nil {
			data["cert_error"] = err.Error()
		}
	case ModeProxy:
		if w.Domain == "" {
			return fail(2, errors.New("a domain is required: the proxy's name, e.g. nubilo.exe.xyz"))
		}
		inv := strings.TrimSpace(w.RelayInvite)
		if inv == "" {
			return fail(2, errors.New("proxy mode needs the invite printed by `nubilo relay init --behind-proxy` on the proxied host"))
		}
		cfg.Public.Domain = w.Domain
		cfg.Public.ACME = config.ACMEConfig{}
		cfg.Public.DirectListen = ""
		cfg.Public.Relay = config.RelayConfig{Invite: inv, Name: server.RelayHomeName, BehindProxy: true}
		data["domain"] = w.Domain
		path, err := probeRelay(ctx, inv, p.Wired)
		if err != nil {
			return fail(2, fmt.Errorf("joining the relay: %w", err))
		}
		data["relay_path"] = path
	case ModeRelay, ModeDirect:
		if w.Domain == "" {
			return fail(2, errors.New("a domain is required (e.g. cloud.example.com, with DNS pointing at the relay or this host)"))
		}
		cfg.Public.Domain = w.Domain
		cfg.Public.ACME = config.ACMEConfig{Enabled: true, Email: strings.TrimSpace(w.Email)}
		if w.Staging {
			cfg.Public.ACME.Directory = config.LetsEncryptStaging
		}
		data["domain"] = w.Domain
		if w.Mode == ModeRelay {
			inv := strings.TrimSpace(w.RelayInvite)
			if inv == "" {
				return fail(2, errors.New("relay mode needs the invite printed by `nubilo relay init` on the VPS"))
			}
			cfg.Public.Relay = config.RelayConfig{Invite: inv, Name: server.RelayHomeName}
			cfg.Public.DirectListen = ""
			path, err := probeRelay(ctx, inv, p.Wired)
			if err != nil {
				return fail(2, fmt.Errorf("joining the relay: %w", err))
			}
			data["relay_path"] = path
		} else {
			cfg.Public.Relay = config.RelayConfig{}
			cfg.Public.DirectListen = ":443"
		}
	default:
		return fail(2, fmt.Errorf("unknown mode %q (relay, proxy, tailscale, direct or lan)", w.Mode))
	}
	if err := cfg.Validate(); err != nil {
		return fail(2, err)
	}
	if err := cfg.Save(p.Config); err != nil {
		return fail(2, err)
	}
	msg := "LAN only (self-signed certificate)"
	switch w.Mode {
	case ModeLAN:
	case ModeTailscale:
		msg = fmt.Sprintf("tailnet devices use %s (certificate from tailscale cert)", cfg.PublicURL())
		if e, ok := data["cert_error"]; ok {
			msg = fmt.Sprintf("tailnet devices use %s; no certificate yet (%v), so the self-signed one is served. As a non-root user run: sudo tailscale set --operator=$USER", cfg.PublicURL(), e)
		}
	case ModeProxy:
		msg = fmt.Sprintf("%s via a relay behind its TLS proxy (the proxy holds the certificate and can read traffic)", w.Domain)
		if path, ok := data["relay_path"]; ok {
			msg += fmt.Sprintf(" (relay path: %v)", path)
		}
	default:
		msg = fmt.Sprintf("%s via %s, Let's Encrypt certificate", w.Domain, w.Mode)
		if path, ok := data["relay_path"]; ok {
			msg += fmt.Sprintf(" (relay path: %v)", path)
		}
	}
	return StepResult{Step: 2, Name: "reachability", OK: true, Message: msg, Data: data}
}

// probeRelay joins the overlay with the home node's state and reports how
// traffic to the relay flows.
func probeRelay(ctx context.Context, invite, stateDir string) (string, error) {
	node, err := wiredx.Join(wiredx.JoinOptions{Name: server.RelayHomeName, Invite: invite, StateDir: stateDir})
	if err != nil {
		return "", err
	}
	defer node.Close()
	relay := ""
	for _, m := range node.Members() {
		if m.Anchor {
			relay = m.Name
		}
	}
	if relay == "" {
		return "unknown", nil
	}
	path := node.Path(relay)
	for i := 0; i < 20 && path == "none" && ctx.Err() == nil; i++ {
		time.Sleep(250 * time.Millisecond)
		path = node.Path(relay)
	}
	return path, nil
}

// StepService installs (or reinstalls) the always-on server service.
func (w *Wizard) StepService() StepResult {
	path, err := InstallServerService(w.DataDir)
	if err != nil {
		return fail(3, err)
	}
	return StepResult{Step: 3, Name: "service", OK: true, Message: "service installed: " + path, Data: map[string]any{"path": path}}
}

// StepVerify waits until the server answers on its public URL (or the LAN
// listener) and reports the certificate issuer. The first request through
// the domain triggers certificate issuance, which can take a minute.
func (w *Wizard) StepVerify(ctx context.Context) StepResult {
	cfg, err := config.Load(config.Paths(w.DataDir).Config)
	if err != nil {
		return fail(4, err)
	}
	url, insecure := cfg.PublicURL(), cfg.Public.ACME.Directory != ""
	if url == "" {
		host, port, _ := net.SplitHostPort(cfg.Listen)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		url, insecure = "https://"+net.JoinHostPort(host, port), true
	}
	timeout := w.VerifyTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // LAN self-signed / LE staging
	}}
	var last error
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/status", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK {
				issuer := ""
				if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
					c := resp.TLS.PeerCertificates[0]
					issuer = c.Issuer.CommonName
					if len(c.Issuer.Organization) > 0 {
						issuer = c.Issuer.Organization[0] + " " + issuer
					}
				}
				return StepResult{Step: 4, Name: "verify", OK: true,
					Message: fmt.Sprintf("%s answers (certificate issuer: %s)", url, issuer),
					Data:    map[string]any{"url": url, "issuer": issuer}}
			}
			last = fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return fail(4, fmt.Errorf("%s not reachable: %v (check DNS, the relay, and `nubilo doctor`)", url, last))
		case <-time.After(3 * time.Second):
		}
	}
}

// StepInvite creates a 24-hour admin invite and its setup QR code.
func (w *Wizard) StepInvite() StepResult {
	rt, err := app.Open(w.DataDir)
	if err != nil {
		return fail(5, err)
	}
	defer rt.Close()
	code, inv, err := rt.IDs.CreateInvite(context.Background(), "setup", identity.UserAdmin, "", 24*time.Hour, 1)
	if err != nil {
		return fail(5, err)
	}
	base := rt.Cfg.DeviceBaseURL()
	setupURL := server.SetupURL(base, code, base64.RawURLEncoding.EncodeToString(rt.ServerPub), "")
	text, _ := qr.Terminal(setupURL)
	png, _ := qr.PNGDataURL(setupURL, 320)
	return StepResult{Step: 5, Name: "invite", OK: true,
		Message: "scan with the Nubilo app to become the first admin",
		Data: map[string]any{
			"code": code, "setup_url": setupURL, "url": server.InviteURL(base, code),
			"expires_at": inv.ExpiresAt, "qr_text": text, "qr_png": png,
		}}
}
