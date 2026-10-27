package relay

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"nubilo/internal/wiredx"
)

// EdgeHandler serves the one port a TLS-terminating proxy (exe.dev) forwards
// to: /relay and /ping go to the local wired relay (peers reach the overlay
// through it), everything else to the home server's overlay web service
// through dial. The client address from the proxy's X-Forwarded-For goes
// home in wiredx.ClientIPHeader; whatever the client sent in it is replaced.
func EdgeHandler(relayBind string, dial func(context.Context) (net.Conn, error), log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	relayTarget := &url.URL{Scheme: "http", Host: relayBind}
	toRelay := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(relayTarget)
			pr.Out.Host = pr.In.Host
		},
	}
	homeTarget := &url.URL{Scheme: "http", Host: "home"}
	toHome := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(homeTarget)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Set(wiredx.ClientIPHeader, edgeClientIP(pr.In))
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		Transport: &http.Transport{
			DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
		},
		// Stream responses (photo downloads, long polls) as they arrive.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("edge_upstream", "err", err.Error(), "path", r.URL.Path)
			http.Error(w, "home server unreachable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/relay" || r.URL.Path == "/ping" {
			toRelay.ServeHTTP(w, r)
			return
		}
		toHome.ServeHTTP(w, r)
	})
}

// edgeClientIP is the address the proxy saw: the last X-Forwarded-For entry
// (the one the proxy appended; earlier ones come from the client), else the
// connection's peer.
func edgeClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
