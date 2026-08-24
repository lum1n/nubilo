package dav_test

import (
	"strings"
	"testing"

	"nubilo/internal/dav"
)

func TestNormalizeRejectsTraversal(t *testing.T) {
	bads := []string{
		"/dav/files/../etc/passwd",
		"/dav/files/foo/../../secret",
		"/dav/files/.",
		"/dav/files/foo/.",
		"/dav/\\files",
		"/dav/files/\x00x",
		strings.Repeat("a", 300),
	}
	for _, p := range bads {
		if _, err := dav.Normalize(p); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}

func TestNormalizeOK(t *testing.T) {
	p, err := dav.Normalize("/dav/files/Docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/dav/files/Docs/a.txt" {
		t.Fatal(p)
	}
	p, err = dav.Normalize("/dav/files//Docs/")
	if err != nil || p != "/dav/files/Docs" {
		t.Fatalf("%s %v", p, err)
	}
}

func TestValidDisplayName(t *testing.T) {
	if dav.ValidDisplayName("..") || dav.ValidDisplayName("a/b") || dav.ValidDisplayName("") {
		t.Fatal("invalid names accepted")
	}
	if !dav.ValidDisplayName("Photo 1.HEIC") {
		t.Fatal("unicode/space name")
	}
}

func TestDAVResourceNameSlash(t *testing.T) {
	if dav.DAVResourceName("a/b", ".ics") != "a_b.ics" {
		t.Fatalf("%q", dav.DAVResourceName("a/b", ".ics"))
	}
}
