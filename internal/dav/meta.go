package dav

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
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
	Name     string `json:"name"`
	UID      string `json:"uid"`
	FN       string `json:"fn,omitempty"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Birthday string `json:"birthday,omitempty"`
}

// ContactMetaFromVCard fills display fields used by UI/listings without reading blobs again.
func ContactMetaFromVCard(fileName, uid string, vcf []byte) ContactMeta {
	m := ContactMeta{Name: fileName, UID: uid}
	card, err := vcard.NewDecoder(bytes.NewReader(vcf)).Decode()
	if err != nil || card == nil {
		return m
	}
	if uid == "" {
		m.UID = strings.TrimSpace(card.Value(vcard.FieldUID))
	}
	m.FN = strings.TrimSpace(card.Value(vcard.FieldFormattedName))
	if m.FN == "" {
		if n := card.Name(); n != nil {
			m.FN = strings.TrimSpace(strings.TrimSpace(n.GivenName) + " " + strings.TrimSpace(n.FamilyName))
		}
	}
	if em := card.PreferredValue(vcard.FieldEmail); em != "" {
		m.Email = strings.TrimSpace(em)
	}
	if tel := card.PreferredValue(vcard.FieldTelephone); tel != "" {
		m.Phone = strings.TrimSpace(tel)
	}
	if b := strings.TrimSpace(card.Value(vcard.FieldBirthday)); b != "" {
		if i := strings.IndexAny(b, "T "); i > 0 {
			b = b[:i]
		}
		m.Birthday = b
	}
	return m
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

// DAVResourceName makes a collection or object name safe for CalDAV/CardDAV hrefs.
// EventKit UIDs and calendar titles can contain slashes; Apple Calendar then fails
// the whole account with "Calendars could not update".
func DAVResourceName(name, ext string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r < 32, r == 0, r == '/', r == '\\':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), " .")
	if name == "" || name == "." || name == ".." {
		name = "item"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	if ext != "" && !strings.HasSuffix(strings.ToLower(name), strings.ToLower(ext)) {
		name += ext
	}
	if !ValidDisplayName(name) {
		if ext == "" {
			return "Untitled"
		}
		return "item" + ext
	}
	return name
}
