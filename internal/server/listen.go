package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"nubilo/internal/acme"
	"nubilo/internal/config"
	"nubilo/internal/tailscale"
	"nubilo/internal/wiredx"
)

// Run serves the API on every configured listener until ctx ends or one
// fails:
//
//   - the LAN listener (cfg.Listen), always;
//   - public.tailscale.funnel_listen, a loopback listener for Tailscale Funnel
//     that reads the client address from a PROXY v1 header;
//   - public.direct_listen (:443) plus :80 when the host is reachable directly;
//   - the wired overlay services "https" and "http" when public.relay.invite is
//     set; the VPS relay forwards public 443/80 to them as raw TCP. With
//     public.relay.behind_proxy (exe.dev) it is the plain-HTTP "web" service
//     instead, since the proxy in front of the relay terminates TLS.
//
// Public listeners use the ACME certificate for public.domain and send HSTS.
func (s *Server) Run(ctx context.Context) error {
	mgr, err := acme.Manager(s.Cfg.Public, config.Paths(s.Cfg.DataDir).ACME)
	if err != nil {
		return err
	}
	var self *tls.Certificate
	if s.Cfg.TLS.Cert != "" && s.Cfg.TLS.Key != "" {
		c, err := tls.LoadX509KeyPair(s.Cfg.TLS.Cert, s.Cfg.TLS.Key)
		if err != nil {
			return fmt.Errorf("server: tls: %w", err)
		}
		self = &c
	}
	var byName func(string) *tls.Certificate
	if ts := s.tailscaleCerts(ctx); ts != nil {
		byName = ts.Get
	}
	tlsCfg := TLSConfig(self, mgr, s.Cfg.Public.Domain, byName)
	errCh := make(chan error, 7)
	serve := func(name string, f func() error) {
		go func() {
			err := f()
			if err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
				errCh <- fmt.Errorf("%s: %w", name, err)
				return
			}
			errCh <- nil
		}()
	}
	running := 0

	ln, err := net.Listen("tcp", s.Cfg.Listen)
	if err != nil {
		return err
	}
	useTLS := self != nil
	s.Log.Info("listen", "addr", s.Cfg.Listen, "tls", useTLS)
	if !useTLS && !s.Cfg.Loopback() {
		ln.Close()
		return errors.New("server: TLS required for non-loopback")
	}
	if useTLS {
		s.http.TLSConfig = tlsCfg
		serve("lan", func() error { return s.http.ServeTLS(ln, "", "") })
	} else {
		serve("lan", func() error { return s.http.Serve(ln) })
	}
	running++

	if ts := s.Cfg.Public.Tailscale; ts.Enabled && ts.FunnelListen != "" {
		fl, err := net.Listen("tcp", ts.FunnelListen)
		if err != nil {
			return fmt.Errorf("server: funnel listen %s: %w", ts.FunnelListen, err)
		}
		// tailscaled sends the public client's address in a PROXY header
		// (tailscale funnel --proxy-protocol 1). Config keeps this listener
		// on loopback, so only processes on this host can set it; conns
		// without one are dropped rather than passing as local.
		fl = wiredx.RequireProxyListener(fl)
		h := s.publicServer(tlsCfg)
		serve("funnel", func() error { return h.ServeTLS(fl, "", "") })
		running++
		s.Log.Info("listen_funnel", "addr", ts.FunnelListen, "name", ts.Name)
	}

	if addr := s.Cfg.Public.DirectListen; addr != "" {
		hl, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("server: direct listen %s: %w", addr, err)
		}
		h := s.publicServer(tlsCfg)
		serve("direct-https", func() error { return h.ServeTLS(hl, "", "") })
		running++
		httpAddr := s.Cfg.Public.DirectHTTPListen
		if httpAddr == "" {
			httpAddr = ":80"
		}
		if pl, err := net.Listen("tcp", httpAddr); err != nil {
			s.Log.Warn("direct_http_listen", "addr", httpAddr, "err", err.Error())
		} else {
			p := s.plainServer(mgr)
			serve("direct-http", func() error { return p.Serve(pl) })
			running++
		}
		s.Log.Info("listen_public", "addr", addr, "http", httpAddr, "domain", s.Cfg.Public.Domain)
	}

	if inv := s.Cfg.Public.Relay.Invite; inv != "" {
		node, err := s.joinRelay()
		switch {
		case err != nil:
			s.Log.Error("relay_join", "err", err.Error())
		case s.Cfg.Public.Relay.BehindProxy:
			// The proxy in front of the relay terminated TLS; plain HTTP
			// arrives over the (encrypted) overlay.
			wl, err := node.Listen(wiredx.ServiceWeb)
			if err != nil {
				return fmt.Errorf("server: wired listen: %w", err)
			}
			h := s.edgeServer()
			serve("relay-web", func() error { return h.Serve(wl) })
			running++
			s.Log.Info("listen_relay", "node", node.Name(), "domain", s.Cfg.Public.Domain, "behind_proxy", true)
		default:
			wl, err := node.Listen(wiredx.ServiceHTTPS)
			if err != nil {
				return fmt.Errorf("server: wired listen: %w", err)
			}
			// The relay sends the client address in a PROXY header.
			wl = wiredx.ProxyListener(wl)
			h := s.publicServer(tlsCfg)
			serve("relay-https", func() error { return h.ServeTLS(wl, "", "") })
			running++
			if pl, err := node.Listen(wiredx.ServiceHTTP); err == nil {
				pl = wiredx.ProxyListener(pl)
				p := s.plainServer(mgr)
				serve("relay-http", func() error { return p.Serve(pl) })
				running++
			}
			s.Log.Info("listen_relay", "node", node.Name(), "domain", s.Cfg.Public.Domain)
		}
	}

	go func() {
		<-ctx.Done()
		shctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shctx)
	}()
	for i := 0; i < running; i++ {
		if err := <-errCh; err != nil {
			shctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Shutdown(shctx)
			cancel()
			return err
		}
	}
	return http.ErrServerClosed
}

func (s *Server) joinRelay() (wiredx.Node, error) {
	name := s.Cfg.Public.Relay.Name
	if name == "" {
		name = RelayHomeName
	}
	dir := s.Cfg.Public.Relay.StateDir
	if dir == "" {
		dir = config.Paths(s.Cfg.DataDir).Wired
	}
	node, err := wiredx.Join(wiredx.JoinOptions{Name: name, Invite: s.Cfg.Public.Relay.Invite, StateDir: dir})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.node = node
	s.mu.Unlock()
	return node, nil
}

// RelayHomeName is the default overlay node name of the home server; the
// relay forwards to https@home and http@home.
const RelayHomeName = "home"

// publicServer serves the API with HSTS for the public domain.
func (s *Server) publicServer(tlsCfg *tls.Config) *http.Server {
	next := s.http.Handler
	h := &http.Server{
		Handler:           hsts(next),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    s.Cfg.HTTP.MaxHeaderBytes,
	}
	s.mu.Lock()
	s.extra = append(s.extra, h)
	s.mu.Unlock()
	return h
}

// edgeServer serves the API to a relay behind a TLS-terminating proxy. The
// relay puts the client address in wiredx.ClientIPHeader; it is trusted
// here, on the overlay listener only (overlay members are the operator's own
// nodes), and dropped everywhere else by the relay overwriting it.
func (s *Server) edgeServer() *http.Server {
	next := s.http.Handler
	h := &http.Server{
		Handler:           EdgeClientIP(hsts(next)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    s.Cfg.HTTP.MaxHeaderBytes,
	}
	s.mu.Lock()
	s.extra = append(s.extra, h)
	s.mu.Unlock()
	return h
}

// EdgeClientIP sets r.RemoteAddr from the relay's client-address header.
func EdgeClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get(wiredx.ClientIPHeader))); ip != nil {
			r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
		}
		r.Header.Del(wiredx.ClientIPHeader)
		next.ServeHTTP(w, r)
	})
}

func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// tailscaleCerts loads the certificate for the Tailscale name in the
// background and keeps it fresh. Until one loads, the Tailscale name gets
// the self-signed certificate like any LAN address.
func (s *Server) tailscaleCerts(ctx context.Context) *tailscale.Certs {
	ts := s.Cfg.Public.Tailscale
	if !ts.Enabled || ts.Name == "" {
		return nil
	}
	c := &tailscale.Certs{Name: ts.Name, Dir: config.Paths(s.Cfg.DataDir).Tailscale, Log: s.Log}
	// The first certificate can take a while to issue; serve the
	// self-signed one meanwhile.
	go func() {
		if err := c.Refresh(ctx); err != nil {
			s.Log.Warn("tailscale_cert", "name", ts.Name, "err", err.Error(),
				"hint", "turn on HTTPS certificates in the Tailscale admin console; as a non-root user run: sudo tailscale set --operator=$USER")
		} else {
			s.Log.Info("tailscale_cert", "name", ts.Name)
		}
		c.Keep(ctx, 24*time.Hour)
	}()
	return c
}

// plainServer answers ACME HTTP-01 challenges and redirects everything else to HTTPS.
func (s *Server) plainServer(mgr *autocert.Manager) *http.Server {
	domain := s.Cfg.Public.Domain
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := domain
		if host == "" {
			host = strings.Split(r.Host, ":")[0]
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
	if mgr != nil {
		h = mgr.HTTPHandler(h)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	s.mu.Lock()
	s.extra = append(s.extra, srv)
	s.mu.Unlock()
	return srv
}
