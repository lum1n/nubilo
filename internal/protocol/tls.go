package protocol

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

	ncrypto "nubilo/internal/crypto"
)

// TLS is client TLS policy for pairing and /sync/v1.
//
// Public CA / Tailscale Serve: leave PinPEM empty; system roots are used.
// Auto self-signed Nubilo certs: pair records PinPEM (TOFU); later requests
// require that exact leaf. --insecure skips both.
type TLS struct {
	Insecure bool
	PinPEM   string
	// OnPeer is invoked after handshake. systemTrusted is true when the
	// chain verified against the system roots (do not persist a pin).
	OnPeer func(pem string, systemTrusted bool)
}

func HTTPClient(timeout time.Duration, pol TLS) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // verification is in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPeer(pol, cs)
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: cfg,
		},
	}
}

func verifyPeer(pol TLS, cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return fmt.Errorf("tls: no server certificate")
	}
	leaf := cs.PeerCertificates[0]
	pem := ncrypto.EncodeCertPEM(leaf)
	if pol.Insecure {
		if pol.OnPeer != nil {
			pol.OnPeer(pem, false)
		}
		return nil
	}
	if pol.PinPEM != "" {
		pin, err := ncrypto.ParseCertPEM(pol.PinPEM)
		if err != nil {
			return err
		}
		if !bytes.Equal(pin.Raw, leaf.Raw) {
			return fmt.Errorf("tls: server certificate does not match the pin stored at pairing")
		}
		return nil
	}
	intermediates := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	opts := x509.VerifyOptions{Intermediates: intermediates}
	if cs.ServerName != "" {
		opts.DNSName = cs.ServerName
	}
	if _, err := leaf.Verify(opts); err == nil {
		if pol.OnPeer != nil {
			pol.OnPeer("", true)
		}
		return nil
	}
	if pol.OnPeer != nil {
		pol.OnPeer(pem, false)
	}
	return nil
}
