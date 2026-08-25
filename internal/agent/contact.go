package agent

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"
)

// ContactSpec is the subset of a contact we sync: name, emails, phones, addresses, birthday.
type ContactSpec struct {
	UID       string
	FN        string
	Given     string
	Family    string
	Emails    []ContactValue
	Phones    []ContactValue
	Addresses []ContactAddress
	// Birthday is YYYY-MM-DD, or --MM-DD when the year is unknown.
	Birthday string
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
	if fn != "" {
		card.SetValue(vcard.FieldFormattedName, fn)
	}
	if s.Given != "" || s.Family != "" {
		card.SetName(&vcard.Name{
			FamilyName: strings.TrimSpace(s.Family),
			GivenName:  strings.TrimSpace(s.Given),
		})
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
	if b := NormalizeBirthday(s.Birthday); b != "" {
		card.SetValue(vcard.FieldBirthday, b)
	}
	var buf bytes.Buffer
	_ = vcard.NewEncoder(&buf).Encode(card)
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
	if n := card.Name(); n != nil {
		out.Family = strings.TrimSpace(n.FamilyName)
		out.Given = strings.TrimSpace(n.GivenName)
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
	out.Birthday = NormalizeBirthday(card.Value(vcard.FieldBirthday))
	return out
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

// DisplayName prefers FN, then given+family.
func (s ContactSpec) DisplayName() string {
	if strings.TrimSpace(s.FN) != "" {
		return strings.TrimSpace(s.FN)
	}
	return strings.TrimSpace(strings.TrimSpace(s.Given) + " " + strings.TrimSpace(s.Family))
}
