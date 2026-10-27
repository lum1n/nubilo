package server

import (
	"crypto/tls"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/acme/autocert"

	"nubilo/internal/acme"
	ncrypto "nubilo/internal/crypto"
)

func TestTLSConfigFallsBackToSelfSigned(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := ncrypto.GenerateTLS(cert, key, []string{"127.0.0.1"}, 0); err != nil {
		t.Fatal(err)
	}
	self, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := TLSConfig(&self, nil, "cloud.example.com", nil)
	got, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "cloud.example.com"})
	if err != nil || got != &self {
		t.Fatalf("without ACME the domain gets the LAN cert: %v", err)
	}
	// With ACME, LAN names keep the self-signed cert; only the domain (and
	// ALPN challenges) go to the manager.
	mgr := &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(t.TempDir()), HostPolicy: autocert.HostWhitelist("cloud.example.com")}
	cfg = TLSConfig(&self, mgr, "cloud.example.com", nil)
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "192.168.1.5"}); got != &self {
		t.Fatal("LAN hello did not get the self-signed cert")
	}
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example.org", SupportedProtos: []string{acme.ALPNProto}}); got == &self {
		t.Fatal("ALPN challenge was not routed to the ACME manager")
	}
	found := false
	for _, p := range cfg.NextProtos {
		found = found || p == acme.ALPNProto
	}
	if !found {
		t.Fatal("acme-tls/1 not offered")
	}
}

func TestTLSConfigServesTailscaleName(t *testing.T) {
	dir := t.TempDir()
	load := func(name string) *tls.Certificate {
		cert, key := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
		if err := ncrypto.GenerateTLS(cert, key, []string{name}, 0); err != nil {
			t.Fatal(err)
		}
		c, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		return &c
	}
	self, ts := load("127.0.0.1"), load("box.tail1234.ts.net")
	byName := func(sni string) *tls.Certificate {
		if sni == "box.tail1234.ts.net" {
			return ts
		}
		return nil
	}
	cfg := TLSConfig(self, nil, "", byName)
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "box.tail1234.ts.net"}); got != ts {
		t.Fatal("the Tailscale name did not get the Tailscale certificate")
	}
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "192.168.1.5"}); got != self {
		t.Fatal("LAN hello did not get the self-signed cert")
	}
}
