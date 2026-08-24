package ids

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

// New returns a new ULID string. Safe for concurrent use.
func New() string {
	entropyMu.Lock()
	defer entropyMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// Valid reports whether s is a ULID.
func Valid(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil
}

// NewWithEntropy is used by tests that need deterministic IDs.
func NewWithEntropy(ms uint64, r io.Reader) string {
	return ulid.MustNew(ms, r).String()
}
