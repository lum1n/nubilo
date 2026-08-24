package dav

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type FileMeta struct {
	Name string `json:"name"`
	MIME string `json:"mime,omitempty"`
}

type EventMeta struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
	Comp string `json:"comp,omitempty"`
}

func ParseEventMeta(raw json.RawMessage) EventMeta {
	var m EventMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func EncodeEventMeta(m EventMeta) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

type ContactMeta struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

func ParseContactMeta(raw json.RawMessage) ContactMeta {
	var m ContactMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func EncodeContactMeta(m ContactMeta) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func ParseFileMeta(raw json.RawMessage) FileMeta {
	var m FileMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func EncodeFileMeta(m FileMeta) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func ValidDisplayName(name string) bool {
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
