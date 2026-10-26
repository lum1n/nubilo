package store

import (
	"encoding/json"
	"strings"
)

// IsBlobID reports whether s is a SHA-256 hex content hash.
func IsBlobID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func metadataBlobKey(k string) bool {
	for _, want := range []string{"preview_hash", "thumb_hash", "live_pair_hash"} {
		if strings.EqualFold(k, want) {
			return true
		}
	}
	return false
}

// MetadataBlobIDs returns extra blob hashes stored in object metadata
// (preview_hash, thumb_hash, live_pair_hash). These are not objects.blob_id but must
// stay live for as long as the object does.
func MetadataBlobIDs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	// Keys match case-insensitively, the way encoding/json fills photos.Meta,
	// so "Preview_Hash" cannot slip a blob past the reference checks.
	for k, v := range m {
		if !metadataBlobKey(k) {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.ToLower(strings.TrimSpace(s))
		if !IsBlobID(s) {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
