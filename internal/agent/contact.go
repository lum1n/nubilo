package agent

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"
)

// ContactSpec is the contact fields we read/write through Contacts.app.
// Unknown vCard properties (and PHOTO when Mac has no image) are preserved
// via MergeContactVCard against the last full server blob.
type ContactSpec struct {
	UID       string
	FN        string
	Given     string
	Family    string
	Org       string
	Nickname  string
	Note      string
	Emails    []ContactValue
	Phones    []ContactValue
	Addresses []ContactAddress
	URLs      []ContactValue
	// Birthday is YYYY-MM-DD, or --MM-DD when the year is unknown.
	Birthday string
	// Photo is raw image bytes when present (encoded as PHOTO;ENCODING=b).
	Photo []byte
}

type ContactValue struct {
	Label string // home, work, cell, other, …
	Value string
}

type ContactAddress struct {
	Label   string
	Street  string
	City    string
	Region  string
	Postal  string
	Country string
}

var (
	bdayFull     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	bdayYearless = regexp.MustCompile(`^--(\d{2})-(\d{2})$`)
	bdayCompact  = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})$`)
)

// managedContactFields are replaced on merge; everything else is kept from base.
var managedContactFields = map[string]bool{
	vcard.FieldVersion:       true,
	vcard.FieldUID:           true,
	vcard.FieldFormattedName: true,
	vcard.FieldName:          true,
	vcard.FieldNickname:      true,
	vcard.FieldOrganization:  true,
	vcard.FieldNote:          true,
	vcard.FieldEmail:         true,
	vcard.FieldTelephone:     true,
	vcard.FieldAddress:       true,
	vcard.FieldBirthday:      true,
	vcard.FieldURL:           true,
	vcard.FieldPhoto:         true,
}

// EncodeContactVCard builds a vCard 3.0 payload from ContactSpec.
func EncodeContactVCard(s ContactSpec) []byte {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "3.0")
	if s.UID != "" {
		card.SetValue(vcard.FieldUID, s.UID)
	}
	fn := strings.TrimSpace(s.FN)
	if fn == "" {
		fn = strings.TrimSpace(strings.TrimSpace(s.Given) + " " + strings.TrimSpace(s.Family))
	}
	if fn == "" {
		fn = strings.TrimSpace(s.Org)
	}
	if fn == "" {
		fn = strings.TrimSpace(s.Nickname)
	}
	if fn != "" {
		card.SetValue(vcard.FieldFormattedName, fn)
	}
	if s.Given != "" || s.Family != "" {
		card.SetName(&vcard.Name{
			FamilyName: strings.TrimSpace(s.Family),
			GivenName:  strings.TrimSpace(s.Given),
		})
	}
	if n := strings.TrimSpace(s.Nickname); n != "" {
		card.SetValue(vcard.FieldNickname, n)
	}
	if o := strings.TrimSpace(s.Org); o != "" {
		card.SetValue(vcard.FieldOrganization, o)
	}
	if n := strings.TrimSpace(s.Note); n != "" {
		card.SetValue(vcard.FieldNote, n)
	}
	for _, e := range s.Emails {
		v := strings.TrimSpace(e.Value)
		if v == "" {
			continue
		}
		f := &vcard.Field{Value: v, Params: vcard.Params{}}
		if t := normalizeContactLabel(e.Label); t != "" {
			f.Params.Add(vcard.ParamType, t)
		}
		card.Add(vcard.FieldEmail, f)
	}
	for _, p := range s.Phones {
		v := strings.TrimSpace(p.Value)
		if v == "" {
			continue
		}
		f := &vcard.Field{Value: v, Params: vcard.Params{}}
		if t := normalizeContactLabel(p.Label); t != "" {
			f.Params.Add(vcard.ParamType, t)
		}
		card.Add(vcard.FieldTelephone, f)
	}
	for _, a := range s.Addresses {
		if a.Street == "" && a.City == "" && a.Region == "" && a.Postal == "" && a.Country == "" {
			continue
		}
		addr := &vcard.Address{
			StreetAddress: strings.TrimSpace(a.Street),
			Locality:      strings.TrimSpace(a.City),
			Region:        strings.TrimSpace(a.Region),
			PostalCode:    strings.TrimSpace(a.Postal),
			Country:       strings.TrimSpace(a.Country),
			Field:         &vcard.Field{Params: vcard.Params{}},
		}
		if t := normalizeContactLabel(a.Label); t != "" {
			addr.Params.Add(vcard.ParamType, t)
		}
		card.AddAddress(addr)
	}
	for _, u := range s.URLs {
		v := strings.TrimSpace(u.Value)
		if v == "" {
			continue
		}
		f := &vcard.Field{Value: v, Params: vcard.Params{}}
		if t := normalizeContactLabel(u.Label); t != "" {
			f.Params.Add(vcard.ParamType, t)
		}
		card.Add(vcard.FieldURL, f)
	}
	if b := NormalizeBirthday(s.Birthday); b != "" {
		card.SetValue(vcard.FieldBirthday, b)
	}
	if len(s.Photo) > 0 {
		f := &vcard.Field{
			Value:  base64.StdEncoding.EncodeToString(s.Photo),
			Params: vcard.Params{},
		}
		f.Params.Set("ENCODING", "b")
		f.Params.Set("TYPE", "JPEG")
		card.Add(vcard.FieldPhoto, f)
	}
	var buf bytes.Buffer
	_ = vcard.NewEncoder(&buf).Encode(card)
	return buf.Bytes()
}

// MergeContactVCard overlays managed fields from spec onto base, preserving
// any other properties (and PHOTO when spec has none). If base is empty,
// returns EncodeContactVCard(spec).
func MergeContactVCard(base []byte, spec ContactSpec) []byte {
	if len(base) == 0 {
		return EncodeContactVCard(spec)
	}
	baseCard, err := vcard.NewDecoder(bytes.NewReader(base)).Decode()
	if err != nil || baseCard == nil {
		return EncodeContactVCard(spec)
	}
	if spec.UID == "" {
		spec.UID = strings.TrimSpace(baseCard.Value(vcard.FieldUID))
	}
	if len(spec.Photo) == 0 {
		if raw := photoFromCard(baseCard); len(raw) > 0 {
			spec.Photo = raw
		}
	}
	overlay := EncodeContactVCard(spec)
	overCard, err := vcard.NewDecoder(bytes.NewReader(overlay)).Decode()
	if err != nil || overCard == nil {
		return overlay
	}
	merged := make(vcard.Card)
	for k, fields := range baseCard {
		if managedContactFields[k] {
			continue
		}
		for _, f := range fields {
			merged.Add(k, f)
		}
	}
	for k, fields := range overCard {
		for _, f := range fields {
			merged.Add(k, f)
		}
	}
	var buf bytes.Buffer
	_ = vcard.NewEncoder(&buf).Encode(merged)
	return buf.Bytes()
}

// ParseContactVCard extracts ContactSpec from a vCard payload.
func ParseContactVCard(vcf []byte) ContactSpec {
	var out ContactSpec
	card, err := vcard.NewDecoder(bytes.NewReader(vcf)).Decode()
	if err != nil || card == nil {
		return out
	}
	out.UID = strings.TrimSpace(card.Value(vcard.FieldUID))
	out.FN = strings.TrimSpace(card.Value(vcard.FieldFormattedName))
	out.Org = strings.TrimSpace(card.Value(vcard.FieldOrganization))
	out.Nickname = strings.TrimSpace(card.Value(vcard.FieldNickname))
	out.Note = strings.TrimSpace(card.Value(vcard.FieldNote))
	if n := card.Name(); n != nil {
		out.Family = strings.TrimSpace(n.FamilyName)
		out.Given = strings.TrimSpace(n.GivenName)
	}
	// FN-only cards: ensure Contacts.app has a displayable name component.
	if out.Given == "" && out.Family == "" && out.FN != "" {
		out.Given = out.FN
	}
	for _, f := range card[vcard.FieldEmail] {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		out.Emails = append(out.Emails, ContactValue{Label: fieldTypeLabel(f), Value: v})
	}
	for _, f := range card[vcard.FieldTelephone] {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		out.Phones = append(out.Phones, ContactValue{Label: fieldTypeLabel(f), Value: v})
	}
	for _, a := range card.Addresses() {
		if a == nil {
			continue
		}
		ca := ContactAddress{
			Label:   fieldTypeLabel(a.Field),
			Street:  strings.TrimSpace(a.StreetAddress),
			City:    strings.TrimSpace(a.Locality),
			Region:  strings.TrimSpace(a.Region),
			Postal:  strings.TrimSpace(a.PostalCode),
			Country: strings.TrimSpace(a.Country),
		}
		if ca.Street == "" && ca.City == "" && ca.Region == "" && ca.Postal == "" && ca.Country == "" {
			continue
		}
		out.Addresses = append(out.Addresses, ca)
	}
	for _, f := range card[vcard.FieldURL] {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		out.URLs = append(out.URLs, ContactValue{Label: fieldTypeLabel(f), Value: v})
	}
	out.Birthday = NormalizeBirthday(card.Value(vcard.FieldBirthday))
	out.Photo = photoFromCard(card)
	return out
}

func photoFromCard(card vcard.Card) []byte {
	for _, f := range card[vcard.FieldPhoto] {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		// Skip URI photos (http/https/data handled poorly by Contacts subset).
		if strings.Contains(v, "://") || strings.HasPrefix(strings.ToLower(v), "data:") {
			continue
		}
		enc := strings.ToLower(f.Params.Get("ENCODING"))
		if enc == "b" || enc == "base64" || !strings.Contains(v, "/") {
			raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(v, " ", ""))
			if err == nil && len(raw) > 0 {
				return raw
			}
			raw, err = base64.RawStdEncoding.DecodeString(strings.ReplaceAll(v, " ", ""))
			if err == nil && len(raw) > 0 {
				return raw
			}
		}
	}
	return nil
}

// NormalizeBirthday accepts YYYY-MM-DD, YYYYMMDD, or --MM-DD and returns a canonical form.
func NormalizeBirthday(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "T "); i > 0 {
		s = s[:i]
	}
	if m := bdayFull.FindStringSubmatch(s); m != nil {
		if !validMD(m[2], m[3]) {
			return ""
		}
		return m[1] + "-" + m[2] + "-" + m[3]
	}
	if m := bdayYearless.FindStringSubmatch(s); m != nil {
		if !validMD(m[1], m[2]) {
			return ""
		}
		return "--" + m[1] + "-" + m[2]
	}
	if m := bdayCompact.FindStringSubmatch(s); m != nil {
		if !validMD(m[2], m[3]) {
			return ""
		}
		return m[1] + "-" + m[2] + "-" + m[3]
	}
	if len(s) == 6 && strings.HasPrefix(s, "--") {
		mm, dd := s[2:4], s[4:6]
		if validMD(mm, dd) {
			return "--" + mm + "-" + dd
		}
	}
	return ""
}

func validMD(mm, dd string) bool {
	m, err1 := strconv.Atoi(mm)
	d, err2 := strconv.Atoi(dd)
	if err1 != nil || err2 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return false
	}
	return true
}

// FormatBirthdayParts builds a canonical birthday from calendar components.
func FormatBirthdayParts(year, month, day int, hasYear bool) string {
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	if hasYear && year > 0 {
		return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	}
	return fmt.Sprintf("--%02d-%02d", month, day)
}

func normalizeContactLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "other":
		return s
	case "mobile", "iphone", "cellphone":
		return "cell"
	case "main":
		return "main"
	case "home", "work", "cell", "fax", "pager":
		return s
	default:
		if len(s) > 32 {
			return "other"
		}
		return s
	}
}

func fieldTypeLabel(f *vcard.Field) string {
	if f == nil {
		return ""
	}
	types := f.Params.Types()
	for _, t := range types {
		t = strings.ToLower(t)
		switch t {
		case "pref", "internet", "voice":
			continue
		default:
			return normalizeContactLabel(t)
		}
	}
	return ""
}

// PreferredEmail returns the first email value, if any.
func (s ContactSpec) PreferredEmail() string {
	if len(s.Emails) == 0 {
		return ""
	}
	return s.Emails[0].Value
}

// PreferredPhone returns the first phone value, if any.
func (s ContactSpec) PreferredPhone() string {
	if len(s.Phones) == 0 {
		return ""
	}
	return s.Phones[0].Value
}

// DisplayName prefers FN, then given+family, then org/nickname.
func (s ContactSpec) DisplayName() string {
	if strings.TrimSpace(s.FN) != "" {
		return strings.TrimSpace(s.FN)
	}
	n := strings.TrimSpace(strings.TrimSpace(s.Given) + " " + strings.TrimSpace(s.Family))
	if n != "" {
		return n
	}
	if strings.TrimSpace(s.Org) != "" {
		return strings.TrimSpace(s.Org)
	}
	return strings.TrimSpace(s.Nickname)
}
