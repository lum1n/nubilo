package profile

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Sign wraps content in a CMS (PKCS #7) SignedData with the content
// attached, as iOS and macOS expect for a signed .mobileconfig. The signer
// is cert's leaf; every certificate in cert.Certificate (leaf first, then
// the intermediates) goes into the message so the device can build the
// path to a root it trusts. The digest is SHA-256 and the signed attributes
// are contentType, messageDigest and signingTime. ECDSA and RSA keys are
// supported.
//
// The encoder is written here with encoding/asn1 rather than taken from a
// pkcs7 package: signing needs only a handful of fixed structures, and the
// maintained packages carry parsing, decryption and legacy algorithms the
// server never uses.
func Sign(content []byte, cert *tls.Certificate) ([]byte, error) {
	if cert == nil || len(cert.Certificate) == 0 {
		return nil, errors.New("profile: no signing certificate")
	}
	leaf := cert.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fmt.Errorf("profile: signing certificate: %w", err)
		}
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("profile: signing key cannot sign")
	}
	var sigAlg []byte
	switch signer.Public().(type) {
	case *ecdsa.PublicKey:
		sigAlg = algID(oidECDSAWithSHA256, false)
	case *rsa.PublicKey:
		sigAlg = algID(oidRSAEncryption, true)
	default:
		return nil, fmt.Errorf("profile: unsupported signing key %T", signer.Public())
	}
	if !publicKeysEqual(signer.Public(), leaf.PublicKey) {
		return nil, errors.New("profile: signing key does not match the certificate")
	}

	sum := sha256.Sum256(content)
	attrs := [][]byte{
		attribute(oidContentType, mustMarshal(oidData)),
		attribute(oidMessageDigest, mustMarshal(sum[:])),
		attribute(oidSigningTime, mustMarshal(time.Now().UTC().Truncate(time.Second))),
	}
	// DER orders SET OF members by their encoding. The signature covers the
	// attributes encoded as a SET; the SignerInfo carries the same bytes
	// under the [0] IMPLICIT tag.
	slices.SortFunc(attrs, bytes.Compare)
	attrsSet := tlv(0x31, attrs...)
	attrsDigest := sha256.Sum256(attrsSet)
	sig, err := signer.Sign(rand.Reader, attrsDigest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("profile: sign: %w", err)
	}

	digestAlg := algID(oidSHA256, false)
	signerInfo := tlv(0x30,
		mustMarshal(1),
		tlv(0x30, leaf.RawIssuer, mustMarshal(leaf.SerialNumber)),
		digestAlg,
		tlv(0xa0, attrs...),
		sigAlg,
		tlv(0x04, sig),
	)
	signedData := tlv(0x30,
		mustMarshal(1),
		tlv(0x31, digestAlg),
		tlv(0x30, mustMarshal(oidData), tlv(0xa0, tlv(0x04, content))),
		tlv(0xa0, cert.Certificate...),
		tlv(0x31, signerInfo),
	)
	return tlv(0x30, mustMarshal(oidSignedData), tlv(0xa0, signedData)), nil
}

var (
	oidData            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidContentType     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSHA256          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSAEncryption   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidECDSAWithSHA256 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
)

func publicKeysEqual(a, b crypto.PublicKey) bool {
	k, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && k.Equal(b)
}

// algID encodes an AlgorithmIdentifier, with NULL parameters when null.
func algID(oid asn1.ObjectIdentifier, null bool) []byte {
	if null {
		return tlv(0x30, mustMarshal(oid), asn1.NullBytes)
	}
	return tlv(0x30, mustMarshal(oid))
}

// attribute encodes an Attribute with a single value.
func attribute(oid asn1.ObjectIdentifier, value []byte) []byte {
	return tlv(0x30, mustMarshal(oid), tlv(0x31, value))
}

func mustMarshal(v any) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err) // only fixed, known-good values are marshalled here
	}
	return b
}

// tlv encodes a DER element with the given identifier octet.
func tlv(tag byte, contents ...[]byte) []byte {
	n := 0
	for _, c := range contents {
		n += len(c)
	}
	out := []byte{tag}
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	default:
		var l []byte
		for v := n; v > 0; v >>= 8 {
			l = append([]byte{byte(v)}, l...)
		}
		out = append(out, 0x80|byte(len(l)))
		out = append(out, l...)
	}
	for _, c := range contents {
		out = append(out, c...)
	}
	return out
}
