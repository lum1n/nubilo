// Package crypto implements the small set of primitives Nubilo uses.
// It does not invent constructions: Ed25519, HKDF-SHA-256, ChaCha20-Poly1305, Argon2id.
package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	MasterKeySize = 32
	BlobKeyInfo   = "nubilo-blob-v1"
	BackupKeyInfo = "nubilo-backup-v1"

	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// Argon2 parameters. Tests may lower Memory to keep the suite fast.
var (
	Argon2Time    uint32 = 3
	Argon2Memory  uint32 = 64 * 1024 // KiB
	Argon2Threads uint8  = 1
	Argon2KeyLen  uint32 = 32
)

var (
	ErrKeySize      = errors.New("crypto: unexpected key size")
	ErrDecrypt      = errors.New("crypto: decryption failed")
	ErrVerifyFailed = errors.New("crypto: verification failed")
)

// Random returns n cryptographically random bytes.
func Random(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("crypto: random: %w", err)
	}
	return b, nil
}

// GenerateMasterKey returns a new 32-byte master key.
func GenerateMasterKey() ([]byte, error) {
	return Random(MasterKeySize)
}

// WriteKeyFile writes key to path with mode 0600.
func WriteKeyFile(path string, key []byte) error {
	return os.WriteFile(path, key, 0o600)
}

// ReadKeyFile reads a key file and checks its size.
func ReadKeyFile(path string, want int) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != want {
		return nil, fmt.Errorf("%w: got %d want %d", ErrKeySize, len(b), want)
	}
	return b, nil
}

// DeriveKey HKDF-SHA-256 expands master into a 32-byte key labeled by info.
func DeriveKey(master []byte, info string) ([]byte, error) {
	if len(master) != MasterKeySize {
		return nil, ErrKeySize
	}
	r := hkdf.New(sha256.New, master, nil, []byte(info))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("crypto: hkdf: %w", err)
	}
	return out, nil
}

// EncryptBlob encrypts plaintext with ChaCha20-Poly1305.
// Layout: 12-byte random nonce || ciphertext || 16-byte tag.
func EncryptBlob(key, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	nonce, err := Random(chacha20poly1305.NonceSize)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptBlob reverses EncryptBlob.
func DecryptBlob(key, blob []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < chacha20poly1305.NonceSize+chacha20poly1305.Overhead {
		return nil, ErrDecrypt
	}
	nonce := blob[:chacha20poly1305.NonceSize]
	ct := blob[chacha20poly1305.NonceSize:]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// SHA256Hex returns the hex-encoded SHA-256 of p.
func SHA256Hex(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

// GenerateEd25519 returns a new device keypair.
func GenerateEd25519() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto: ed25519: %w", err)
	}
	return pub, priv, nil
}

// SignEd25519 signs msg with priv.
func SignEd25519(priv ed25519.PrivateKey, msg []byte) []byte {
	return ed25519.Sign(priv, msg)
}

// VerifyEd25519 reports whether sig is valid.
func VerifyEd25519(pub ed25519.PublicKey, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

func PublicKeyB64(pub ed25519.PublicKey) string {
	return base64.RawStdEncoding.EncodeToString(pub)
}

func ParsePublicKeyB64(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("crypto: public key: %w", err)
		}
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("crypto: public key: %w", ErrKeySize)
	}
	return ed25519.PublicKey(b), nil
}

func PrivateKeyBytes(priv ed25519.PrivateKey) []byte {
	return []byte(priv)
}

func ParsePrivateKey(b []byte) (ed25519.PrivateKey, error) {
	if len(b) != ed25519.PrivateKeySize {
		return nil, ErrKeySize
	}
	return ed25519.PrivateKey(b), nil
}

// HashSecret Argon2id-hashes secret with the given salt.
func HashSecret(secret, salt []byte) []byte {
	return argon2.IDKey(secret, salt, Argon2Time, Argon2Memory, Argon2Threads, Argon2KeyLen)
}

// NewSalt returns a 16-byte salt.
func NewSalt() ([]byte, error) {
	return Random(16)
}

// VerifySecret compares Argon2id(secret, salt) to hash in constant time.
func VerifySecret(secret, salt, hash []byte) error {
	got := HashSecret(secret, salt)
	if subtle.ConstantTimeCompare(got, hash) != 1 {
		return ErrVerifyFailed
	}
	return nil
}

// PairingCode returns a 10-character Crockford Base32 code (XXXXX-XXXXX form when formatted).
func PairingCode() (string, error) {
	raw, err := Random(8)
	if err != nil {
		return "", err
	}
	var n uint64
	for _, b := range raw {
		n = (n << 8) | uint64(b)
	}
	out := make([]byte, 10)
	for i := 9; i >= 0; i-- {
		out[i] = crockford[n&31]
		n >>= 5
	}
	return string(out), nil
}

// FormatPairingCode inserts a dash: XXXXX-XXXXX.
func FormatPairingCode(code string) string {
	if len(code) != 10 {
		return code
	}
	return code[:5] + "-" + code[5:]
}

// NormalizePairingCode strips dashes/spaces and uppercases.
func NormalizePairingCode(s string) string {
	out := make([]byte, 0, 10)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' || c == ' ' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		out = append(out, c)
	}
	return string(out)
}

// AppPassword returns a 26-character Crockford password (from 16 random bytes).
func AppPassword() (string, error) {
	raw, err := Random(16)
	if err != nil {
		return "", err
	}
	var n [2]uint64
	for i := 0; i < 8; i++ {
		n[0] = (n[0] << 8) | uint64(raw[i])
		n[1] = (n[1] << 8) | uint64(raw[8+i])
	}
	out := make([]byte, 26)
	v := n[0]
	for i := 12; i >= 0; i-- {
		out[i] = crockford[v&31]
		v >>= 5
	}
	v = n[1]
	for i := 25; i >= 13; i-- {
		out[i] = crockford[v&31]
		v >>= 5
	}
	return string(out), nil
}
