package profile

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nubilo/internal/config"
)

// testPKI is a root CA, an intermediate and a leaf issued like a Let's
// Encrypt certificate (serverAuth only).
type testPKI struct {
	root  *x509.Certificate
	roots *x509.CertPool
	leaf  *tls.Certificate // chain: leaf, intermediate
}

func newKey(t *testing.T, rsaKey bool) crypto.Signer {
	t.Helper()
	if rsaKey {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func issue(t *testing.T, tmpl, parent *x509.Certificate, pub crypto.PublicKey, signer crypto.Signer) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newPKI(t *testing.T, rsaLeaf bool) *testPKI {
	t.Helper()
	now := time.Now()
	ca := func(serial int64, name string) *x509.Certificate {
		return &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		}
	}
	rootKey := newKey(t, false)
	root := issue(t, ca(1, "Test Root"), ca(1, "Test Root"), rootKey.Public(), rootKey)
	interKey := newKey(t, false)
	inter := issue(t, ca(2, "Test Intermediate"), root, interKey.Public(), rootKey)
	leafKey := newKey(t, rsaLeaf)
	leaf := issue(t, &x509.Certificate{
		SerialNumber: big.NewInt(4242), Subject: pkix.Name{CommonName: "cloud.example.com"},
		DNSNames:  []string{"cloud.example.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, inter, leafKey.Public(), interKey)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return &testPKI{root: root, roots: pool, leaf: &tls.Certificate{
		Certificate: [][]byte{leaf.Raw, inter.Raw}, PrivateKey: leafKey, Leaf: leaf,
	}}
}

// verifyCMS parses a SignedData independently of the encoder and checks
// the content, signed attributes, signature and certificate chain.
func verifyCMS(t *testing.T, der, content []byte, roots *x509.CertPool) {
	t.Helper()
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue // [0] EXPLICIT SignedData
	}
	if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) > 0 || !ci.Type.Equal(oidSignedData) {
		t.Fatalf("ContentInfo: %v %v rest=%d", err, ci.Type, len(rest))
	}
	type attribute struct {
		Type   asn1.ObjectIdentifier
		Values asn1.RawValue
	}
	type signerInfo struct {
		Version int
		SID     struct {
			Issuer asn1.RawValue
			Serial *big.Int
		}
		DigestAlg pkix.AlgorithmIdentifier
		Attrs     asn1.RawValue
		SigAlg    pkix.AlgorithmIdentifier
		Sig       []byte
	}
	var sd struct {
		Version    int
		DigestAlgs []pkix.AlgorithmIdentifier `asn1:"set"`
		Encap      struct {
			Type    asn1.ObjectIdentifier
			Content asn1.RawValue // [0] EXPLICIT OCTET STRING
		}
		Certs       asn1.RawValue
		SignerInfos []signerInfo `asn1:"set"`
	}
	if ci.Content.Class != asn1.ClassContextSpecific || ci.Content.Tag != 0 {
		t.Fatal("ContentInfo content is not [0]")
	}
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		t.Fatalf("SignedData: %v", err)
	}
	if sd.Version != 1 || len(sd.DigestAlgs) != 1 || !sd.DigestAlgs[0].Algorithm.Equal(oidSHA256) {
		t.Fatalf("SignedData header %+v", sd)
	}
	var econtent asn1.RawValue
	if _, err := asn1.Unmarshal(sd.Encap.Content.Bytes, &econtent); err != nil || sd.Encap.Content.Tag != 0 {
		t.Fatalf("eContent: %v", err)
	}
	if !sd.Encap.Type.Equal(oidData) || econtent.Tag != asn1.TagOctetString || !bytes.Equal(econtent.Bytes, content) {
		t.Fatal("encapsulated content is not the profile")
	}
	if sd.Certs.Class != asn1.ClassContextSpecific || sd.Certs.Tag != 0 {
		t.Fatalf("certificates tag %d/%d", sd.Certs.Class, sd.Certs.Tag)
	}
	var certs []*x509.Certificate
	for rest := sd.Certs.Bytes; len(rest) > 0; {
		var raw asn1.RawValue
		var err error
		if rest, err = asn1.Unmarshal(rest, &raw); err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(raw.FullBytes)
		if err != nil {
			t.Fatal(err)
		}
		certs = append(certs, c)
	}
	if len(certs) != 2 {
		t.Fatalf("want leaf and intermediate, got %d certificates", len(certs))
	}
	leaf := certs[0]
	inter := x509.NewCertPool()
	inter.AddCert(certs[1])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(sd.SignerInfos) != 1 {
		t.Fatalf("%d signers", len(sd.SignerInfos))
	}
	si := sd.SignerInfos[0]
	if si.Version != 1 || !bytes.Equal(si.SID.Issuer.FullBytes, leaf.RawIssuer) || si.SID.Serial.Cmp(leaf.SerialNumber) != 0 {
		t.Fatal("signer identifier does not name the leaf")
	}
	if !si.DigestAlg.Algorithm.Equal(oidSHA256) {
		t.Fatalf("digest algorithm %v", si.DigestAlg.Algorithm)
	}
	if si.Attrs.Class != asn1.ClassContextSpecific || si.Attrs.Tag != 0 {
		t.Fatal("signed attributes missing")
	}
	got := map[string]asn1.RawValue{}
	for rest := si.Attrs.Bytes; len(rest) > 0; {
		var a attribute
		var err error
		if rest, err = asn1.Unmarshal(rest, &a); err != nil {
			t.Fatal(err)
		}
		var v asn1.RawValue
		if _, err := asn1.Unmarshal(a.Values.Bytes, &v); err != nil {
			t.Fatal(err)
		}
		got[a.Type.String()] = v
	}
	var ct asn1.ObjectIdentifier
	if _, err := asn1.Unmarshal(got[oidContentType.String()].FullBytes, &ct); err != nil || !ct.Equal(oidData) {
		t.Fatalf("contentType attribute %v %v", ct, err)
	}
	sum := sha256.Sum256(content)
	if v := got[oidMessageDigest.String()]; v.Tag != asn1.TagOctetString || !bytes.Equal(v.Bytes, sum[:]) {
		t.Fatal("messageDigest does not match the content")
	}
	var at time.Time
	if _, err := asn1.Unmarshal(got[oidSigningTime.String()].FullBytes, &at); err != nil || time.Since(at) > time.Minute {
		t.Fatalf("signingTime %v %v", at, err)
	}
	// The signature covers the attributes re-tagged as a SET.
	signed := append([]byte{0x31}, si.Attrs.FullBytes[1:]...)
	alg := x509.ECDSAWithSHA256
	if _, ok := leaf.PublicKey.(*rsa.PublicKey); ok {
		alg = x509.SHA256WithRSA
		if !si.SigAlg.Algorithm.Equal(oidRSAEncryption) {
			t.Fatalf("signature algorithm %v", si.SigAlg.Algorithm)
		}
	} else if !si.SigAlg.Algorithm.Equal(oidECDSAWithSHA256) {
		t.Fatalf("signature algorithm %v", si.SigAlg.Algorithm)
	}
	if err := leaf.CheckSignature(alg, signed, si.Sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
}

// opensslVerify checks der with `openssl cms -verify` when openssl exists.
func opensslVerify(t *testing.T, der, content []byte, root *x509.Certificate) {
	t.Helper()
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Log("openssl not on PATH; skipping openssl verification")
		return
	}
	dir := t.TempDir()
	in, ca, out := filepath.Join(dir, "p.mobileconfig"), filepath.Join(dir, "ca.pem"), filepath.Join(dir, "out")
	if err := os.WriteFile(in, der, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	// -purpose any: like a Let's Encrypt certificate, the leaf is for
	// serverAuth, which iOS accepts for profiles but OpenSSL's S/MIME
	// default does not.
	cmd := exec.Command(bin, "cms", "-verify", "-inform", "DER", "-in", in, "-CAfile", ca, "-purpose", "any", "-out", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl cms -verify: %v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("openssl output differs from the profile: %v", err)
	}
}

func testProfile(t *testing.T) []byte {
	t.Helper()
	b, err := Build(Account{Host: "cloud.example.com", Username: "alice", Password: "pw", CalDAV: true, CardDAV: true})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSign(t *testing.T) {
	for _, tc := range []struct {
		name string
		rsa  bool
	}{{"ecdsa", false}, {"rsa", true}} {
		t.Run(tc.name, func(t *testing.T) {
			pki := newPKI(t, tc.rsa)
			content := testProfile(t)
			der, err := Sign(content, pki.leaf)
			if err != nil {
				t.Fatal(err)
			}
			verifyCMS(t, der, content, pki.roots)
			opensslVerify(t, der, content, pki.root)
		})
	}
}

func TestSignRejectsMismatchedKey(t *testing.T) {
	a, b := newPKI(t, false), newPKI(t, false)
	c := *a.leaf
	c.PrivateKey = b.leaf.PrivateKey
	if _, err := Sign([]byte("x"), &c); err == nil {
		t.Fatal("signed with a key that is not the certificate's")
	}
}

func writePEM(t *testing.T, path string, c *tls.Certificate, withKey bool) {
	t.Helper()
	var b bytes.Buffer
	if withKey {
		var blk *pem.Block
		switch k := c.PrivateKey.(type) {
		case *ecdsa.PrivateKey:
			der, err := x509.MarshalECPrivateKey(k)
			if err != nil {
				t.Fatal(err)
			}
			blk = &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
		case *rsa.PrivateKey:
			blk = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
		}
		_ = pem.Encode(&b, blk)
	}
	for _, der := range c.Certificate {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func selfSigned(t *testing.T) *tls.Certificate {
	t.Helper()
	k := newKey(t, false)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "nubilo.local"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	c := issue(t, tmpl, tmpl, k.Public(), k)
	return &tls.Certificate{Certificate: [][]byte{c.Raw}, PrivateKey: k, Leaf: c}
}

func TestSigningCert(t *testing.T) {
	pki := newPKI(t, false)
	signingRoots = pki.roots
	t.Cleanup(func() { signingRoots = nil })

	relay := func(t *testing.T) config.Config {
		cfg := config.Defaults(t.TempDir())
		cfg.Public.Domain = "cloud.example.com"
		cfg.Public.ACME.Enabled = true
		cfg.Public.Relay.Invite = "invite"
		return cfg
	}

	t.Run("relay uses the ACME certificate", func(t *testing.T) {
		cfg := relay(t)
		writePEM(t, filepath.Join(config.Paths(cfg.DataDir).ACME, "cloud.example.com"), pki.leaf, true)
		c, src, err := SigningCert(cfg)
		if err != nil || c == nil || !strings.Contains(src, "Let's Encrypt") {
			t.Fatalf("got %v %q %v", c, src, err)
		}
		content := testProfile(t)
		der, err := Sign(content, c)
		if err != nil {
			t.Fatal(err)
		}
		verifyCMS(t, der, content, pki.roots)
	})

	t.Run("rsa fallback in the ACME cache", func(t *testing.T) {
		cfg := relay(t)
		rsaPKI := newPKI(t, true)
		signingRoots = rsaPKI.roots
		defer func() { signingRoots = pki.roots }()
		writePEM(t, filepath.Join(config.Paths(cfg.DataDir).ACME, "cloud.example.com+rsa"), rsaPKI.leaf, true)
		if c, _, err := SigningCert(cfg); err != nil || c == nil {
			t.Fatalf("got %v %v", c, err)
		}
	})

	t.Run("proxy mode stays unsigned", func(t *testing.T) {
		cfg := relay(t)
		cfg.Public.Relay.BehindProxy = true
		writePEM(t, filepath.Join(config.Paths(cfg.DataDir).ACME, "cloud.example.com"), pki.leaf, true)
		if c, _, err := SigningCert(cfg); c != nil || err == nil || !strings.Contains(err.Error(), "proxy") {
			t.Fatalf("proxy mode signed: %v %v", c, err)
		}
	})

	t.Run("self-signed LAN certificate is never used", func(t *testing.T) {
		cfg := config.Defaults(t.TempDir())
		ss := selfSigned(t)
		writePEM(t, filepath.Join(cfg.DataDir, "tls.crt"), ss, false)
		if c, _, err := SigningCert(cfg); c != nil || err == nil {
			t.Fatalf("LAN-only server signed: %v %v", c, err)
		}
		if err := Usable(ss, time.Now()); err == nil || !strings.Contains(err.Error(), "self-signed") {
			t.Fatalf("self-signed certificate accepted: %v", err)
		}
	})

	t.Run("untrusted chain is refused", func(t *testing.T) {
		cfg := relay(t)
		writePEM(t, filepath.Join(config.Paths(cfg.DataDir).ACME, "cloud.example.com"), newPKI(t, false).leaf, true)
		if c, _, err := SigningCert(cfg); c != nil || err == nil {
			t.Fatalf("untrusted certificate used: %v %v", c, err)
		}
	})

	t.Run("tailscale certificate", func(t *testing.T) {
		cfg := config.Defaults(t.TempDir())
		cfg.Public.Tailscale.Enabled = true
		cfg.Public.Tailscale.Name = "box.tail1234.ts.net"
		base := filepath.Join(config.Paths(cfg.DataDir).Tailscale, "box.tail1234.ts.net")
		writePEM(t, base+".crt", pki.leaf, false)
		key := &tls.Certificate{PrivateKey: pki.leaf.PrivateKey}
		writePEM(t, base+".key", key, true)
		c, src, err := SigningCert(cfg)
		if err != nil || c == nil || !strings.Contains(src, "Tailscale") {
			t.Fatalf("got %v %q %v", c, src, err)
		}
	})
}
