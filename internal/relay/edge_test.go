package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nubilo/internal/server"
	"nubilo/internal/wiredx"
)

func TestEdgeDefaultConfig(t *testing.T) {
	c, err := EdgeDefaultConfig("https://nubilo.exe.xyz/", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicHost != "nubilo.exe.xyz" || c.Edge.URL != "https://nubilo.exe.xyz" || c.Edge.Listen != EdgeListen ||
		c.Edge.RelayBind != EdgeRelayBind || c.Edge.Upstream != "web@home" || len(c.Forward) != 0 {
		t.Fatalf("config %+v %+v", c, c.Edge)
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	o := c.anchorOptions(t.TempDir())
	if o.RelayURL != "https://nubilo.exe.xyz" || o.RelayBind != EdgeRelayBind || !o.NoQAD {
		t.Fatalf("anchor options %+v", o)
	}
	for _, bad := range []string{"http://nubilo.exe.xyz", "nubilo.exe.xyz", "https://nubilo.exe.xyz/sub"} {
		if _, err := EdgeDefaultConfig(bad, "", ""); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestEdgeHandlerRoutes(t *testing.T) {
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "relay "+r.URL.Path)
	}))
	defer relaySrv.Close()
	// The home side as the server runs it behind a proxy: EdgeClientIP first.
	var gotIP, gotHost, gotHeader string
	home := httptest.NewServer(server.EdgeClientIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIP, _, _ = net.SplitHostPort(r.RemoteAddr)
		gotHost = r.Host
		gotHeader = r.Header.Get(wiredx.ClientIPHeader)
		_, _ = io.WriteString(w, "home "+r.URL.Path)
	})))
	defer home.Close()
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", home.Listener.Addr().String())
	}
	edge := httptest.NewServer(EdgeHandler(relaySrv.Listener.Addr().String(), dial, nil))
	defer edge.Close()

	get := func(path string, hdr map[string]string) string {
		req, _ := http.NewRequest(http.MethodGet, edge.URL+path, nil)
		req.Host = "nubilo.exe.xyz"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get("/ping", nil); got != "relay /ping" {
		t.Fatalf("/ping went to %q", got)
	}
	if got := get("/relay", nil); got != "relay /relay" {
		t.Fatalf("/relay went to %q", got)
	}
	// The client forges both headers; the proxy (exe.dev) appended the real
	// address last. Home must see that one and never the forged header.
	got := get("/api/v1/status", map[string]string{
		wiredx.ClientIPHeader: "6.6.6.6",
		"X-Forwarded-For":     "6.6.6.6, 203.0.113.9",
	})
	if got != "home /api/v1/status" {
		t.Fatalf("api went to %q", got)
	}
	if gotIP != "203.0.113.9" || gotHost != "nubilo.exe.xyz" || gotHeader != "" {
		t.Fatalf("home saw ip=%q host=%q header=%q", gotIP, gotHost, gotHeader)
	}
	// Without X-Forwarded-For the edge reports the proxy connection's address.
	get("/x", nil)
	if gotIP != "127.0.0.1" {
		t.Fatalf("home saw ip=%q without XFF", gotIP)
	}
	if strings.Contains(get("/x", map[string]string{"X-Forwarded-For": "not-an-ip"}), "home") && gotIP != "127.0.0.1" {
		t.Fatalf("bad XFF gave ip=%q", gotIP)
	}
}
