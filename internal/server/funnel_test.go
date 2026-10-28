package server_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"nubilo/internal/audit"
	"nubilo/internal/auth"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/server"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
	"nubilo/internal/tailscale"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// The Funnel listener takes the client address from tailscaled's PROXY
// header, so a public client no longer passes as loopback (admin token).
func TestFunnelListenerUsesProxyAddress(t *testing.T) {
	old := tailscale.Run
	t.Cleanup(func() { tailscale.Run = old })
	tailscale.Run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("no tailscale") }

	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Defaults(dir)
	cfg.Listen = freeAddr(t)
	cfg.TLS.Cert, cfg.TLS.Key = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := ncrypto.GenerateTLS(cfg.TLS.Cert, cfg.TLS.Key, []string{"127.0.0.1"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	cfg.Public.Tailscale = config.TailscaleConfig{Enabled: true, Name: "box.tail1234.ts.net", Port: 443, FunnelListen: freeAddr(t)}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	idsvc := identity.NewService(st)
	a := &auth.Authenticator{IDs: idsvc, Store: st, SkewMS: 60_000, AdminTok: []byte("admintok")}
	pub, _, _ := ncrypto.GenerateEd25519()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, st, idsvc, a, syncengine.New(st), &audit.Logger{Store: st, Slog: log}, log, pub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	get := func(addr, proxyLine string) (int, error) {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				c, err := d.DialContext(ctx, network, addr)
				if err == nil && proxyLine != "" {
					_, err = c.Write([]byte(proxyLine))
				}
				return c, err
			},
		}
		defer tr.CloseIdleConnections()
		req, _ := http.NewRequest("GET", "https://box.tail1234.ts.net/api/v1/admin/stats", nil)
		req.Header.Set("X-Nubilo-Admin", "admintok")
		resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	var code int
	for i := 0; i < 50; i++ {
		if code, err = get(cfg.Listen, ""); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != http.StatusOK {
		t.Fatalf("admin over LAN loopback: %d %v", code, err)
	}
	_, fport, _ := net.SplitHostPort(cfg.Public.Tailscale.FunnelListen)
	code, err = get(cfg.Public.Tailscale.FunnelListen, fmt.Sprintf("PROXY TCP4 203.0.113.9 127.0.0.1 51000 %s\r\n", fport))
	if err != nil || code == http.StatusOK {
		t.Fatalf("admin over funnel from a public IP: %d %v", code, err)
	}
	if _, err := get(cfg.Public.Tailscale.FunnelListen, ""); err == nil {
		t.Fatal("funnel listener served a connection without a PROXY header")
	}
}
