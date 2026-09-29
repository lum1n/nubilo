package agent

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"
)

// ContactSpec is the contact fields we read/write through Contacts.app.
// Unknown vCard properties (and PHOTO when Mac has no image) are preserved
// via MergeContactVCard against the last full server blob.
type ContactSpec struct {
	UID        string
	FN         string
	Given      string
	Family     string
	Middle     string
	Prefix     string
	Suffix     string
	Org        string
	Department string
	Title      string
	Nickname   string
	Note       string
	Emails     []ContactValue
	Phones     []ContactValue
	Addresses  []ContactAddress
	URLs       []ContactValue
	Related    []ContactValue
	Social     []ContactSocial
	IM         []ContactIM
	Dates      []ContactDate
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

type ContactSocial struct {
	Service string
	User    string
	URL     string
}

type ContactIM struct {
	Service string
	User    string
}

type ContactDate struct {
	Label string
	Date  string // YYYY-MM-DD or --MM-DD
}

var (
	bdayFull     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	bdayYearless = regexp.MustCompile(`^--(\d{2})-(\d{2})$`)
	bdayCompact  = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})$`)
)

const (
	fieldABRelated = "X-ABRELATEDNAMES"
	fieldSocial    = "X-SOCIALPROFILE"
	fieldABDate    = "X-ABDATE"
)

// managedContactFields are replaced on merge; everything else is kept from base.
var managedContactFields = map[string]bool{
	vcard.FieldVersion:       true,
	vcard.FieldUID:           true,
	vcard.FieldFormattedName: true,
	vcard.FieldName:          true,
	vcard.FieldNickname:      true,
	vcard.FieldOrganization:  true,
	vcard.FieldTitle:         true,
	vcard.FieldNote:          true,
	vcard.FieldEmail:         true,
	vcard.FieldTelephone:     true,
	vcard.FieldAddress:       true,
	vcard.FieldBirthday:      true,
	vcard.FieldURL:           true,
	vcard.FieldPhoto:         true,
	vcard.FieldIMPP:          true,
	fieldABRelated:           true,
	fieldSocial:              true,
	fieldABDate:              true,
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
	if s.Given != "" || s.Family != "" || s.Middle != "" || s.Prefix != "" || s.Suffix != "" {
		card.SetName(&vcard.Name{
			FamilyName:      strings.TrimSpace(s.Family),
			GivenName:       strings.TrimSpace(s.Given),
			AdditionalName:  strings.TrimSpace(s.Middle),
			HonorificPrefix: strings.TrimSpace(s.Prefix),
			HonorificSuffix: strings.TrimSpace(s.Suffix),
		})
	}
	if n := strings.TrimSpace(s.Nickname); n != "" {
		card.SetValue(vcard.FieldNickname, n)
	}
	org := strings.TrimSpace(s.Org)
	dept := strings.TrimSpace(s.Department)
	if org != "" || dept != "" {
		if dept != "" {
			card.SetValue(vcard.FieldOrganization, org+";"+dept)
		} else {
			card.SetValue(vcard.FieldOrganization, org)
		}
	}
	if t := strings.TrimSpace(s.Title); t != "" {
		card.SetValue(vcard.FieldTitle, t)
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
	for _, r := range s.Related {
		v := strings.TrimSpace(r.Value)
		if v == "" {
			continue
		}
		f := &vcard.Field{Value: v, Params: vcard.Params{}}
		if t := strings.TrimSpace(r.Label); t != "" {
			f.Params.Set("X-ABLABEL", t)
		}
		card.Add(fieldABRelated, f)
	}
	for _, sprofile := range s.Social {
		url := strings.TrimSpace(sprofile.URL)
		user := strings.TrimSpace(sprofile.User)
		svc := strings.TrimSpace(sprofile.Service)
		if url == "" && user == "" {
			continue
		}
		if url == "" {
			url = "x-apple:" + user
		}
		f := &vcard.Field{Value: url, Params: vcard.Params{}}
		if svc != "" {
			f.Params.Set("TYPE", svc)
		}
		if user != "" {
			f.Params.Set("X-USER", user)
		}
		card.Add(fieldSocial, f)
	}
	for _, im := range s.IM {
		user := strings.TrimSpace(im.User)
		if user == "" {
			continue
		}
		svc := strings.ToLower(strings.TrimSpace(im.Service))
		value := user
		if svc != "" && !strings.Contains(user, ":") {
			value = svc + ":" + user
		}
		f := &vcard.Field{Value: value, Params: vcard.Params{}}
		if svc != "" {
			f.Params.Set("X-SERVICE-TYPE", svc)
		}
		card.Add(vcard.FieldIMPP, f)
	}
	for _, d := range s.Dates {
		v := NormalizeBirthday(d.Date)
		if v == "" {
			continue
		}
		f := &vcard.Field{Value: v, Params: vcard.Params{}}
		if t := strings.TrimSpace(d.Label); t != "" {
			f.Params.Set("X-ABLABEL", t)
		}
		card.Add(fieldABDate, f)
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
// returns EncodeContactVCard(spec). The base card's UID wins when present.
func MergeContactVCard(base []byte, spec ContactSpec) []byte {
	if len(base) == 0 {
		return EncodeContactVCard(spec)
	}
	baseCard, err := vcard.NewDecoder(bytes.NewReader(base)).Decode()
	if err != nil || baseCard == nil {
		return EncodeContactVCard(spec)
	}
	if baseUID := strings.TrimSpace(baseCard.Value(vcard.FieldUID)); baseUID != "" {
		spec.UID = baseUID
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
	out.Org, out.Department = splitOrg(card.Value(vcard.FieldOrganization))
	out.Title = strings.TrimSpace(card.Value(vcard.FieldTitle))
	out.Nickname = strings.TrimSpace(card.Value(vcard.FieldNickname))
	out.Note = strings.TrimSpace(card.Value(vcard.FieldNote))
	if n := card.Name(); n != nil {
		out.Family = strings.TrimSpace(n.FamilyName)
		out.Given = strings.TrimSpace(n.GivenName)
		out.Middle = strings.TrimSpace(n.AdditionalName)
		out.Prefix = strings.TrimSpace(n.HonorificPrefix)
		out.Suffix = strings.TrimSpace(n.HonorificSuffix)
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
	for _, f := range card[fieldABRelated] {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		label := strings.TrimSpace(f.Params.Get("X-ABLABEL"))
		if label == "" {
			label = fieldTypeLabel(f)
		}
		out.Related = append(out.Related, ContactValue{Label: label, Value: v})
	}
	for _, f := range card[fieldSocial] {
		url := strings.TrimSpace(f.Value)
		user := strings.TrimSpace(f.Params.Get("X-USER"))
		svc := strings.TrimSpace(f.Params.Get("TYPE"))
		if svc == "" {
			types := f.Params.Types()
			if len(types) > 0 {
				svc = types[0]
			}
		}
		if url == "" && user == "" {
			continue
		}
		out.Social = append(out.Social, ContactSocial{Service: svc, User: user, URL: url})
	}
	for _, f := range card[vcard.FieldIMPP] {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		svc := strings.TrimSpace(f.Params.Get("X-SERVICE-TYPE"))
		user := v
		if i := strings.Index(v, ":"); i >= 0 {
			if svc == "" {
				svc = v[:i]
			}
			user = v[i+1:]
		}
		out.IM = append(out.IM, ContactIM{Service: svc, User: user})
	}
	for _, f := range card[fieldABDate] {
		v := NormalizeBirthday(f.Value)
		if v == "" {
			continue
		}
		label := strings.TrimSpace(f.Params.Get("X-ABLABEL"))
		out.Dates = append(out.Dates, ContactDate{Label: label, Date: v})
	}
	out.Birthday = NormalizeBirthday(card.Value(vcard.FieldBirthday))
	out.Photo = photoFromCard(card)
	return out
}

func splitOrg(s string) (org, department string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	parts := strings.SplitN(s, ";", 2)
	org = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		department = strings.TrimSpace(parts[1])
	}
	return org, department
}

// ContactFingerprint is a stable match key for remapping local IDs.
func ContactFingerprint(spec ContactSpec) string {
	fn := strings.ToLower(strings.TrimSpace(spec.DisplayName()))
	emails := make([]string, 0, len(spec.Emails))
	for _, e := range spec.Emails {
		v := strings.ToLower(strings.TrimSpace(e.Value))
		if v != "" {
			emails = append(emails, v)
		}
	}
	sort.Strings(emails)
	phones := make([]string, 0, len(spec.Phones))
	for _, p := range spec.Phones {
		v := strings.ToLower(strings.TrimSpace(p.Value))
		if v != "" {
			phones = append(phones, v)
		}
	}
	sort.Strings(phones)
	email, phone := "", ""
	if len(emails) > 0 {
		email = emails[0]
	}
	if len(phones) > 0 {
		phone = phones[0]
	}
	return fn + "|" + email + "|" + phone
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
