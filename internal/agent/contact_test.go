package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestContactVCardRoundTrip(t *testing.T) {
	in := ContactSpec{
		UID:    "uid-1",
		FN:     "Ada Lovelace",
		Given:  "Ada",
		Family: "Lovelace",
		Org:    "Analytical Engine",
		Note:   "Poetical scientist",
		Emails: []ContactValue{
			{Label: "work", Value: "ada@analytical.engine"},
			{Label: "home", Value: "ada@home.example"},
		},
		Phones: []ContactValue{
			{Label: "mobile", Value: "+44 7700 900123"},
			{Label: "work", Value: "+44 20 7946 0958"},
		},
		Addresses: []ContactAddress{{
			Label:   "home",
			Street:  "12 St James's Square",
			City:    "London",
			Region:  "England",
			Postal:  "SW1Y 4LE",
			Country: "United Kingdom",
		}},
		URLs:     []ContactValue{{Label: "work", Value: "https://example.com/ada"}},
		Birthday: "1815-12-10",
		Photo:    []byte{0xff, 0xd8, 0xff, 0xd9},
	}
	raw := EncodeContactVCard(in)
	for _, want := range []string{"EMAIL", "TEL", "ADR", "BDAY", "ORG", "NOTE", "URL", "PHOTO"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("missing %s:\n%s", want, raw)
		}
	}
	out := ParseContactVCard(raw)
	if out.UID != in.UID || out.Given != in.Given || out.Family != in.Family {
		t.Fatalf("name %#v", out)
	}
	if out.Org != in.Org || out.Note != in.Note {
		t.Fatalf("org/note %#v", out)
	}
	if out.Birthday != "1815-12-10" {
		t.Fatalf("birthday %q", out.Birthday)
	}
	if out.PreferredEmail() != "ada@analytical.engine" {
		t.Fatalf("email %q", out.PreferredEmail())
	}
	if out.PreferredPhone() != "+44 7700 900123" {
		t.Fatalf("phone %q", out.PreferredPhone())
	}
	if len(out.Emails) != 2 || out.Emails[0].Label != "work" {
		t.Fatalf("emails %#v", out.Emails)
	}
	if len(out.Phones) != 2 || out.Phones[0].Label != "cell" {
		t.Fatalf("phones %#v", out.Phones)
	}
	if len(out.Addresses) != 1 {
		t.Fatalf("addresses %#v", out.Addresses)
	}
	a := out.Addresses[0]
	if a.Street != in.Addresses[0].Street || a.City != "London" || a.Postal != "SW1Y 4LE" || a.Country != "United Kingdom" {
		t.Fatalf("address %#v", a)
	}
	if len(out.URLs) != 1 || out.URLs[0].Value != "https://example.com/ada" {
		t.Fatalf("urls %#v", out.URLs)
	}
	if !bytes.Equal(out.Photo, in.Photo) {
		t.Fatalf("photo %#v", out.Photo)
	}
}

func TestParseLegacySingleEmailVCard(t *testing.T) {
	raw := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:Grace Hopper\r\nN:Hopper;Grace;;;\r\nEMAIL:grace@navy.mil\r\nEND:VCARD\r\n")
	out := ParseContactVCard(raw)
	if out.DisplayName() != "Grace Hopper" || out.PreferredEmail() != "grace@navy.mil" {
		t.Fatalf("%#v", out)
	}
	if out.Family != "Hopper" || out.Given != "Grace" {
		t.Fatalf("N %#v", out)
	}
}

func TestParseFNOnlySetsGiven(t *testing.T) {
	raw := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:Solo Name\r\nTEL:+1\r\nEND:VCARD\r\n")
	out := ParseContactVCard(raw)
	if out.Given != "Solo Name" || out.DisplayName() != "Solo Name" {
		t.Fatalf("%#v", out)
	}
}

func TestMergeContactVCardPreservesExtras(t *testing.T) {
	base := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:u1\r\nFN:Ada\r\nN:;Ada;;;\r\nEMAIL:old@example.com\r\nNOTE:keep-me\r\nX-CUSTOM:secret\r\nPHOTO;ENCODING=b;TYPE=JPEG:/w==\r\nEND:VCARD\r\n")
	merged := MergeContactVCard(base, ContactSpec{
		UID: "u1", FN: "Ada Lovelace", Given: "Ada", Family: "Lovelace",
		Emails: []ContactValue{{Value: "new@example.com"}},
		Note:   "updated note",
	})
	s := string(merged)
	if !strings.Contains(s, "new@example.com") {
		t.Fatalf("missing new email: %s", s)
	}
	if strings.Contains(s, "old@example.com") {
		t.Fatalf("kept old email: %s", s)
	}
	if !strings.Contains(s, "updated note") {
		t.Fatalf("missing note: %s", s)
	}
	if !strings.Contains(s, "X-CUSTOM:secret") {
		t.Fatalf("lost custom prop: %s", s)
	}
	if !strings.Contains(s, "PHOTO") {
		t.Fatalf("lost photo: %s", s)
	}
	if !strings.Contains(s, "Ada Lovelace") {
		t.Fatalf("missing FN: %s", s)
	}
}

func TestEncodeSkipsEmpty(t *testing.T) {
	raw := EncodeContactVCard(ContactSpec{UID: "u", FN: "Solo"})
	s := string(raw)
	if strings.Contains(s, "EMAIL") || strings.Contains(s, "TEL") || strings.Contains(s, "ADR") || strings.Contains(s, "BDAY") {
		t.Fatalf("%s", s)
	}
}

func TestNormalizeBirthday(t *testing.T) {
	cases := map[string]string{
		"1815-12-10":           "1815-12-10",
		"18151210":             "1815-12-10",
		"--12-10":              "--12-10",
		"--1210":               "--12-10",
		"1815-12-10T00:00:00Z": "1815-12-10",
		"bad":                  "",
		"2024-13-01":           "",
	}
	for in, want := range cases {
		if got := NormalizeBirthday(in); got != want {
			t.Fatalf("%q => %q want %q", in, got, want)
		}
	}
}
