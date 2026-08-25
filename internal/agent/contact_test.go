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
		Birthday: "1815-12-10",
	}
	raw := EncodeContactVCard(in)
	if !bytes.Contains(raw, []byte("EMAIL")) || !bytes.Contains(raw, []byte("TEL")) || !bytes.Contains(raw, []byte("ADR")) || !bytes.Contains(raw, []byte("BDAY")) {
		t.Fatalf("missing fields:\n%s", raw)
	}
	out := ParseContactVCard(raw)
	if out.UID != in.UID || out.Given != in.Given || out.Family != in.Family {
		t.Fatalf("name %#v", out)
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
