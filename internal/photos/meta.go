package photos

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	CollectionKind = "photos"
	ObjectKind     = "photo"
	DefaultName    = "Photos"
)

// Meta is stored in the object metadata JSON. Coordinates are never stored here:
// GPS lives only inside the encrypted original blob.
type Meta struct {
	Name         string   `json:"name"`
	MIME         string   `json:"mime,omitempty"`
	Kind         string   `json:"kind,omitempty"` // image | video | live | raw
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	Orientation  int      `json:"orientation,omitempty"`
	CameraMake   string   `json:"camera_make,omitempty"`
	CameraModel  string   `json:"camera_model,omitempty"`
	TakenAtMS    int64    `json:"taken_at_ms,omitempty"`
	DurationMS   int64    `json:"duration_ms,omitempty"`
	Checksum     string   `json:"checksum,omitempty"`
	PreviewHash  string   `json:"preview_hash,omitempty"`
	ThumbHash    string   `json:"thumb_hash,omitempty"`
	LivePairHash string   `json:"live_pair_hash,omitempty"` // paired Live Photo movie blob
	HasGPS       bool     `json:"has_gps,omitempty"`
	Perceptual   string   `json:"phash,omitempty"`
	Albums       []string `json:"albums,omitempty"`
}

func ParseMeta(raw json.RawMessage) Meta {
	var m Meta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func EncodeMeta(m Meta) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func ValidName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	if strings.ContainsRune(name, 0) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	return true
}
