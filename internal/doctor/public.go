package doctor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"nubilo/internal/config"
	"nubilo/internal/tailscale"
	"nubilo/internal/wiredx"
)

// checkPublic covers public reachability: DNS, the wired relay, the ACME
// certificate, an end-to-end request to https://<domain>, and direct ports.
func checkPublic(ctx context.Context, r *Report, cfg config.Config) {
	pub := cfg.Public
	if pub.Domain == "" && pub.Tailscale.Enabled {
		checkTailscale(ctx, r, cfg)
		return
	}
	if pub.Domain == "" {
		r.add(Check{ID: "public", Title: "public access", Status: Info, Detail: "LAN only (no public domain or Tailscale name)",
			Fix: "nubilo setup  → relay, proxy, tailscale or direct, to reach the server away from home"})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(ctx, pub.Domain)
	if err != nil || len(addrs) == 0 {
		r.add(Check{ID: "dns", Title: "DNS " + pub.Domain, Status: Fail, Detail: fmt.Sprint(err),
			Fix: "create an A/AAAA record for " + pub.Domain + " pointing at the relay VPS (relay) or this host (direct)"})
	} else {
		r.add(Check{ID: "dns", Title: "DNS " + pub.Domain, Status: OK, Detail: strings.Join(addrs, ", ")})
	}

	if pub.Relay.Invite != "" {
		checkRelay(ctx, r, cfg)
	}
	if pub.Relay.BehindProxy {
		r.add(Check{ID: "proxy_tls", Title: "TLS termination", Status: Warn,
			Detail: "the proxy in front of the relay (e.g. exe.dev) holds the certificate for " + pub.Domain + " and can read calendar, contacts, files, photos and app passwords",
			Fix:    "for TLS that ends at home, run `nubilo relay` on a VPS with public ports instead (nubilo setup → relay)"})
	}
	if pub.DirectListen != "" {
		checkDirectPorts(r, pub)
	}
	checkPublicTLS(ctx, r, pub)
}

func checkRelay(ctx context.Context, r *Report, cfg config.Config) {
	if !wiredx.Available() {
		r.add(Check{ID: "relay", Title: "relay", Status: Fail, Detail: "this binary was built without wired support",
			Fix: "install a release build of nubilo (built with -tags wired)"})
		return
	}
	st, err := localStatus(ctx, cfg)
	if err != nil {
		r.add(Check{ID: "relay", Title: "relay", Status: Warn, Detail: "server not answering locally: " + err.Error(),
			Fix: "start the server: nubilo server install"})
		return
	}
	relay, _ := st["relay"].(map[string]any)
	path, _ := relay["path"].(string)
	switch path {
	case "direct", "relay":
		r.add(Check{ID: "relay", Title: "relay", Status: OK, Detail: "connected (" + path + " path)"})
	default:
		fix := "on the VPS: nubilo relay members (is `nubilo relay run` up? ports 7443 tcp/udp, 7842 udp open?)"
		if cfg.Public.Relay.BehindProxy {
			fix = "on the relay host: nubilo relay members (is `nubilo relay run` up? does the proxy forward to its listen port and is it public?)"
		}
		r.add(Check{ID: "relay", Title: "relay", Status: Fail, Detail: "relay: unreachable", Fix: fix})
	}
}

// localStatus asks the running server for /api/v1/status with the admin token.
func localStatus(ctx context.Context, cfg config.Config) (map[string]any, error) {
	tok, err := os.ReadFile(config.Paths(cfg.DataDir).AdminToken)
	if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "https"
	if cfg.TLS.Cert == "" {
		scheme = "http"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+net.JoinHostPort(host, port)+"/api/v1/status", nil)
	req.Header.Set("X-Nubilo-Admin", strings.TrimSpace(string(tok)))
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback to our own self-signed listener
	}}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out := map[string]any{}
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func checkDirectPorts(r *Report, pub config.PublicConfig) {
	for _, addr := range []string{pub.DirectListen, orDefault(pub.DirectHTTPListen, ":80")} {
		_, port, _ := net.SplitHostPort(addr)
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 2*time.Second)
		if err != nil {
			r.add(Check{ID: "ports", Title: "port " + port, Status: Fail, Detail: "nothing listening",
				Fix: "start the server; on Linux allow low ports: sysctl net.ipv4.ip_unprivileged_port_start=80"})
			continue
		}
		c.Close()
		r.add(Check{ID: "ports", Title: "port " + port, Status: OK, Detail: "listening (forward it on your router if behind NAT)"})
	}
}

func checkPublicTLS(ctx context.Context, r *Report, pub config.PublicConfig) {
	staging := pub.ACME.Directory != ""
	c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: pub.Domain, InsecureSkipVerify: staging}, //nolint:gosec // LE staging is untrusted by design
	}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pub.BaseURL()+"/api/v1/status", nil)
	resp, err := c.Do(req)
	if err != nil {
		r.add(Check{ID: "public_reach", Title: "https://" + pub.Domain, Status: Fail, Detail: err.Error(),
			Fix: "check DNS, the relay or port forward, and that the server is running"})
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusOK {
		r.add(Check{ID: "public_reach", Title: "https://" + pub.Domain, Status: Fail, Detail: fmt.Sprintf("HTTP %d (another service answers?)", resp.StatusCode)})
	} else {
		r.add(Check{ID: "public_reach", Title: "https://" + pub.Domain, Status: OK, Detail: "answers from the internet path"})
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return
	}
	cert := resp.TLS.PeerCertificates[0]
	left := time.Until(cert.NotAfter)
	detail := fmt.Sprintf("%s, expires %s", cert.Issuer.CommonName, cert.NotAfter.Format("2006-01-02"))
	switch {
	case staging:
		r.add(Check{ID: "cert", Title: "public certificate", Status: Warn, Detail: detail + " (staging CA: phones will not trust it)",
			Fix: "remove public.acme.directory from config.json and restart the server"})
	case left < 7*24*time.Hour:
		r.add(Check{ID: "cert", Title: "public certificate", Status: Fail, Detail: detail + " — renewal failing?"})
	case left < 21*24*time.Hour:
		r.add(Check{ID: "cert", Title: "public certificate", Status: Warn, Detail: detail})
	default:
		r.add(Check{ID: "cert", Title: "public certificate", Status: OK, Detail: detail})
	}
}

// checkTailscale covers tailscale mode: tailscaled's view of this node and an
// end-to-end request to the MagicDNS URL that must pass system trust.
func checkTailscale(ctx context.Context, r *Report, cfg config.Config) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	name := cfg.Public.Tailscale.Name
	self, err := tailscale.Status(ctx)
	switch {
	case err != nil:
		r.add(Check{ID: "tailscale", Title: "tailscale", Status: Fail, Detail: err.Error(),
			Fix: "install Tailscale and run: sudo tailscale up"})
		return
	case !self.Online:
		r.add(Check{ID: "tailscale", Title: "tailscale", Status: Fail, Detail: "tailscaled is not running or logged out",
			Fix: "sudo tailscale up"})
		return
	case self.Name != name:
		r.add(Check{ID: "tailscale", Title: "tailscale", Status: Warn,
			Detail: fmt.Sprintf("config has %s but this node is %s", name, self.Name),
			Fix:    "nubilo setup --mode tailscale"})
	case !self.CertOK:
		r.add(Check{ID: "tailscale", Title: "tailscale", Status: Warn, Detail: "HTTPS certificates are off for the tailnet",
			Fix: "Tailscale admin console → DNS → enable MagicDNS and HTTPS Certificates"})
	default:
		r.add(Check{ID: "tailscale", Title: "tailscale", Status: OK, Detail: name})
	}
	url := cfg.PublicURL()
	c := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/status", nil)
	resp, err := c.Do(req)
	if err != nil {
		fix := "check the server log for tailscale_cert; as a non-root user run: sudo tailscale set --operator=$USER, then restart the server"
		r.add(Check{ID: "public_reach", Title: url, Status: Warn, Detail: err.Error() + " (devices fall back to the pinned self-signed certificate)", Fix: fix})
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusOK {
		r.add(Check{ID: "public_reach", Title: url, Status: Fail, Detail: fmt.Sprintf("HTTP %d (another service answers?)", resp.StatusCode)})
		return
	}
	detail := "answers with a trusted certificate"
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		detail += ", expires " + resp.TLS.PeerCertificates[0].NotAfter.Format("2006-01-02")
	}
	r.add(Check{ID: "public_reach", Title: url, Status: OK, Detail: detail + " (tailnet devices only)"})
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
