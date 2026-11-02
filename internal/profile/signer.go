package profile

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nubilo/internal/config"
)

// signingRoots replaces the system roots in tests.
var signingRoots *x509.CertPool

// SigningCert returns the certificate that signs profiles for cfg and where
// it comes from, or nil and the reason profiles go out unsigned.
//
// Only a certificate that chains to a public root is worth signing with: a
// device shows such a profile as "Verified", while a self-signed signature
// looks no better than none. The candidates are, in order:
//
//   - the ACME (Let's Encrypt) certificate for public.domain, from the
//     autocert cache, in relay and direct modes. Proxy mode
//     (public.relay.behind_proxy) has no certificate at home.
//   - the Tailscale certificate for the MagicDNS name, from the files
//     `tailscale cert` writes.
//
// The self-signed LAN certificate (tls.crt) is never used.
func SigningCert(cfg config.Config) (*tls.Certificate, string, error) {
	p := config.Paths(cfg.DataDir)
	var reasons []string
	pub := cfg.Public
	switch {
	case strings.TrimSpace(pub.Domain) == "":
	case pub.Relay.BehindProxy:
		reasons = append(reasons, "proxy mode: the certificate for "+pub.Domain+" is on the proxy")
	case !pub.ACME.Enabled:
		reasons = append(reasons, "ACME is off for "+pub.Domain)
	default:
		// autocert caches the ECDSA certificate under the bare domain and
		// the RSA fallback under domain+"+rsa".
		var errs []error
		for _, name := range []string{pub.Domain, pub.Domain + "+rsa"} {
			c, err := loadPEMPair(filepath.Join(p.ACME, name), filepath.Join(p.ACME, name))
			if err == nil {
				err = Usable(c, time.Now())
			}
			if err == nil {
				return c, "Let's Encrypt certificate for " + pub.Domain, nil
			}
			errs = append(errs, err)
		}
		reasons = append(reasons, "no usable ACME certificate for "+pub.Domain+": "+errs[0].Error())
	}
	if ts := pub.Tailscale; ts.Enabled && ts.Name != "" {
		base := filepath.Join(p.Tailscale, ts.Name)
		c, err := loadPEMPair(base+".crt", base+".key")
		if err == nil {
			err = Usable(c, time.Now())
		}
		if err == nil {
			return c, "Tailscale certificate for " + ts.Name, nil
		}
		reasons = append(reasons, "no usable Tailscale certificate for "+ts.Name+": "+err.Error())
	}
	if len(reasons) == 0 {
		return nil, "", errors.New("no public domain or Tailscale name; the LAN certificate is self-signed")
	}
	return nil, "", errors.New(strings.Join(reasons, "; "))
}

// Usable reports why c cannot sign profiles, or nil when it can: it must be
// valid now and chain to a trusted root through the certificates it
// carries. A self-signed certificate never qualifies.
func Usable(c *tls.Certificate, now time.Time) error {
	if c == nil || len(c.Certificate) == 0 {
		return errors.New("no certificate")
	}
	leaf := c.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return err
		}
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("certificate is not valid now (expires %s)", leaf.NotAfter.Format("2006-01-02"))
	}
	if leaf.CheckSignatureFrom(leaf) == nil {
		return errors.New("certificate is self-signed")
	}
	inter := x509.NewCertPool()
	for _, der := range c.Certificate[1:] {
		if ic, err := x509.ParseCertificate(der); err == nil {
			inter.AddCert(ic)
		}
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:         signingRoots,
		Intermediates: inter,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	var sysErr x509.SystemRootsError
	if errors.As(err, &sysErr) {
		// No root store on this host (e.g. a bare container); the
		// certificate came from a public CA, so sign anyway.
		return nil
	}
	return err
}

// loadPEMPair loads a certificate chain and key; certFile and keyFile may be
// the same file (autocert stores the key and chain together).
func loadPEMPair(certFile, keyFile string) (*tls.Certificate, error) {
	cb, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	kb := cb
	if keyFile != certFile {
		if kb, err = os.ReadFile(keyFile); err != nil {
			return nil, err
		}
	}
	c, err := tls.X509KeyPair(cb, kb)
	if err != nil {
		return nil, err
	}
	return &c, nil
}
