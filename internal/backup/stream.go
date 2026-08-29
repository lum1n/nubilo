package backup

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	ncrypto "nubilo/internal/crypto"
)

// streamChunkSize is the plaintext size sealed per AEAD frame (tunable in tests).
var streamChunkSize = 1 << 20 // 1 MiB

// SetStreamChunkSizeForTest overrides the frame size; returns the previous value.
func SetStreamChunkSizeForTest(n int) int {
	prev := streamChunkSize
	if n > 0 {
		streamChunkSize = n
	}
	return prev
}

// chunkEncWriter seals plaintext in frames so backups never hold the full
// archive in memory. Frame layout:
//
//	u32be plainLen | nonce(12) | ciphertext(plainLen+16)
//
// plainLen == 0 marks EOF (no nonce/ciphertext follow).
type chunkEncWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	buf    []byte
	closed bool
}

func newChunkEncWriter(w io.Writer, key []byte) (*chunkEncWriter, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &chunkEncWriter{w: w, aead: aead, buf: make([]byte, 0, streamChunkSize)}, nil
}

func (c *chunkEncWriter) Write(p []byte) (int, error) {
	if c.closed {
		return 0, errors.New("backup: write to closed stream")
	}
	n := 0
	for len(p) > 0 {
		space := streamChunkSize - len(c.buf)
		if space > len(p) {
			space = len(p)
		}
		c.buf = append(c.buf, p[:space]...)
		p = p[space:]
		n += space
		if len(c.buf) >= streamChunkSize {
			if err := c.flushChunk(c.buf); err != nil {
				return n, err
			}
			c.buf = c.buf[:0]
		}
	}
	return n, nil
}

func (c *chunkEncWriter) flushChunk(plain []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(plain)))
	if _, err := c.w.Write(lenBuf[:]); err != nil {
		return err
	}
	nonce, err := ncrypto.Random(chacha20poly1305.NonceSize)
	if err != nil {
		return err
	}
	if _, err := c.w.Write(nonce); err != nil {
		return err
	}
	ct := c.aead.Seal(nil, nonce, plain, nil)
	_, err = c.w.Write(ct)
	return err
}

func (c *chunkEncWriter) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if len(c.buf) > 0 {
		if err := c.flushChunk(c.buf); err != nil {
			return err
		}
		c.buf = c.buf[:0]
	}
	var zero [4]byte // plainLen == 0 → EOF
	_, err := c.w.Write(zero[:])
	return err
}

type chunkDecReader struct {
	r    io.Reader
	aead cipher.AEAD
	buf  []byte
	off  int
	eof  bool
}

func newChunkDecReader(r io.Reader, key []byte) (*chunkDecReader, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &chunkDecReader{r: r, aead: aead}, nil
}

func (c *chunkDecReader) Read(p []byte) (int, error) {
	if c.off < len(c.buf) {
		n := copy(p, c.buf[c.off:])
		c.off += n
		return n, nil
	}
	if c.eof {
		return 0, io.EOF
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(c.r, lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, errors.New("backup: truncated stream")
		}
		return 0, err
	}
	plainLen := binary.BigEndian.Uint32(lenBuf[:])
	if plainLen == 0 {
		c.eof = true
		return 0, io.EOF
	}
	const maxPlain = 16 << 20 // 16 MiB hard cap per frame
	if plainLen > maxPlain {
		return 0, errors.New("backup: chunk too large")
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(c.r, nonce); err != nil {
		return 0, err
	}
	ct := make([]byte, int(plainLen)+c.aead.Overhead())
	if _, err := io.ReadFull(c.r, ct); err != nil {
		return 0, err
	}
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return 0, ncrypto.ErrDecrypt
	}
	c.buf = pt
	c.off = 0
	n := copy(p, c.buf)
	c.off = n
	return n, nil
}
