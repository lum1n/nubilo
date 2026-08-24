package protocol_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/protocol"
)

func tlsServer(t *testing.T, hosts []string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := ncrypto.GenerateTLS(certPath, keyPath, hosts, 0); err != nil {
		t.Fatal(err)
	}
	kp, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{kp}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func TestTOFUPinAndMismatch(t *testing.T) {
	ts := tlsServer(t, []string{"127.0.0.1", "localhost"})
	leaf := ts.Certificate()
	pin := ncrypto.EncodeCertPEM(leaf)
	c := protocol.HTTPClient(5*time.Second, protocol.TLS{PinPEM: pin})
	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ts2 := tlsServer(t, []string{"127.0.0.1", "localhost"})
	if _, err := c.Get(ts2.URL); err == nil {
		t.Fatal("pin must reject a different certificate")
	}
}

func TestTOFUCapturesSelfSigned(t *testing.T) {
	ts := tlsServer(t, []string{"127.0.0.1", "localhost"})
	var got string
	var trusted bool
	c := protocol.HTTPClient(5*time.Second, protocol.TLS{OnPeer: func(p string, sys bool) {
		got, trusted = p, sys
	}})
	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if trusted || got == "" {
		t.Fatalf("expected TOFU pin, trusted=%v pem=%d", trusted, len(got))
	}
}
