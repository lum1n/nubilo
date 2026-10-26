package profile

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestBuildIsWellFormedPlist(t *testing.T) {
	b, err := Build(Account{ServerName: "Family <Cloud>", Host: "cloud.example.com", Username: "alice", Password: "p&ss", CalDAV: true, CardDAV: true, Identifier: "srv"})
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("not well-formed: %v\n%s", err, b)
		}
	}
	s := string(b)
	for _, want := range []string{
		"com.apple.caldav.account", "com.apple.carddav.account", "<string>/caldav/u/alice/</string>",
		"<string>/carddav/u/alice/</string>", "<integer>443</integer>", "p&amp;ss", "Family &lt;Cloud&gt;",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	again, _ := Build(Account{ServerName: "Family <Cloud>", Host: "cloud.example.com", Username: "alice", Password: "other", CalDAV: true, CardDAV: true, Identifier: "srv"})
	if uuidFrom("x") != uuidFrom("x") || !strings.Contains(string(again), uuidFrom("dev.nubilo.profile."+stableID("srv|cloud.example.com|alice"))) {
		t.Fatal("payload UUIDs must be stable so reinstalling replaces the account")
	}
}

func TestBuildRequiresService(t *testing.T) {
	if _, err := Build(Account{Host: "h", Username: "u", Password: "p"}); err == nil {
		t.Fatal("no services accepted")
	}
}
