package server

import (
	"crypto/tls"
	"slices"
	"strings"

	"golang.org/x/crypto/acme/autocert"

	"nubilo/internal/acme"
)

// TLSConfig serves the ACME certificate for the public domain (and answers
// TLS-ALPN-01 challenges) and the self-signed LAN certificate otherwise, so
// one listener works for both https://<domain> and https://<lan-ip>.
func TLSConfig(self *tls.Certificate, mgr *autocert.Manager, domain string) *tls.Config {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1", acme.ALPNProto},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			sni := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
			if mgr != nil && domain != "" && (sni == domain || slices.Contains(hello.SupportedProtos, acme.ALPNProto)) {
				return mgr.GetCertificate(hello)
			}
			if self == nil {
				if mgr != nil {
					return mgr.GetCertificate(hello)
				}
				return nil, errNoCertificate
			}
			return self, nil
		},
	}
}

type tlsError string

func (e tlsError) Error() string { return string(e) }

const errNoCertificate = tlsError("server: no certificate for this host")
