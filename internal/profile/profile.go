// Package profile builds Apple configuration profiles (.mobileconfig) that
// add Nubilo as native CalDAV (Calendar, Reminders) and CardDAV (Contacts)
// accounts on iOS and macOS.
package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strings"
)

// Account describes the server account the profile installs.
type Account struct {
	ServerName string // shown in Settings, e.g. "Nubilo"
	Host       string // DNS name or IP, without scheme or port
	Port       int    // 443 for a public domain
	Username   string
	Password   string // app password; the profile holds it in plaintext
	CalDAV     bool
	CardDAV    bool
	// Identifier makes payload identifiers stable per server+user so
	// reinstalling replaces the old profile instead of duplicating accounts.
	Identifier string
}

// PrincipalPath returns the CalDAV principal of username.
func PrincipalPath(prefix, username string) string {
	return prefix + "/u/" + username + "/"
}

// Build returns the unsigned profile XML.
func Build(a Account) ([]byte, error) {
	if a.Host == "" || a.Username == "" || a.Password == "" {
		return nil, fmt.Errorf("profile: host, username and password are required")
	}
	if !a.CalDAV && !a.CardDAV {
		return nil, fmt.Errorf("profile: no services selected")
	}
	if a.Port == 0 {
		a.Port = 443
	}
	if a.ServerName == "" {
		a.ServerName = "Nubilo"
	}
	base := "dev.nubilo.profile." + stableID(a.Identifier+"|"+a.Host+"|"+a.Username)
	var payloads []dict
	if a.CalDAV {
		payloads = append(payloads, dict{
			{"PayloadType", "com.apple.caldav.account"},
			{"PayloadVersion", 1},
			{"PayloadIdentifier", base + ".caldav"},
			{"PayloadUUID", uuidFrom(base + ".caldav")},
			{"PayloadDisplayName", a.ServerName + " Calendar"},
			{"CalDAVAccountDescription", a.ServerName},
			{"CalDAVHostName", a.Host},
			{"CalDAVPort", a.Port},
			{"CalDAVUseSSL", true},
			{"CalDAVUsername", a.Username},
			{"CalDAVPassword", a.Password},
			{"CalDAVPrincipalURL", PrincipalPath("/caldav", a.Username)},
		})
	}
	if a.CardDAV {
		payloads = append(payloads, dict{
			{"PayloadType", "com.apple.carddav.account"},
			{"PayloadVersion", 1},
			{"PayloadIdentifier", base + ".carddav"},
			{"PayloadUUID", uuidFrom(base + ".carddav")},
			{"PayloadDisplayName", a.ServerName + " Contacts"},
			{"CardDAVAccountDescription", a.ServerName},
			{"CardDAVHostName", a.Host},
			{"CardDAVPort", a.Port},
			{"CardDAVUseSSL", true},
			{"CardDAVUsername", a.Username},
			{"CardDAVPassword", a.Password},
			{"CardDAVPrincipalURL", PrincipalPath("/carddav", a.Username)},
		})
	}
	root := dict{
		{"PayloadType", "Configuration"},
		{"PayloadVersion", 1},
		{"PayloadIdentifier", base},
		{"PayloadUUID", uuidFrom(base)},
		{"PayloadDisplayName", a.ServerName + " (" + a.Username + ")"},
		{"PayloadDescription", "Adds your " + a.ServerName + " calendars, reminders and contacts."},
		{"PayloadOrganization", a.ServerName},
		{"PayloadRemovalDisallowed", false},
		{"PayloadContent", payloads},
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	if err := writeValue(&b, root, 0); err != nil {
		return nil, err
	}
	b.WriteString("</plist>\n")
	return b.Bytes(), nil
}

// ContentType is the MIME type iOS Safari hands to the profile installer.
const ContentType = "application/x-apple-aspen-config"

type kv struct {
	k string
	v any
}

type dict []kv

func writeValue(b *bytes.Buffer, v any, depth int) error {
	ind := strings.Repeat("\t", depth)
	switch x := v.(type) {
	case dict:
		b.WriteString(ind + "<dict>\n")
		for _, e := range x {
			b.WriteString(ind + "\t<key>")
			_ = xml.EscapeText(b, []byte(e.k))
			b.WriteString("</key>\n")
			if err := writeValue(b, e.v, depth+1); err != nil {
				return err
			}
		}
		b.WriteString(ind + "</dict>\n")
	case []dict:
		b.WriteString(ind + "<array>\n")
		for _, d := range x {
			if err := writeValue(b, d, depth+1); err != nil {
				return err
			}
		}
		b.WriteString(ind + "</array>\n")
	case string:
		b.WriteString(ind + "<string>")
		_ = xml.EscapeText(b, []byte(x))
		b.WriteString("</string>\n")
	case int:
		fmt.Fprintf(b, "%s<integer>%d</integer>\n", ind, x)
	case bool:
		if x {
			b.WriteString(ind + "<true/>\n")
		} else {
			b.WriteString(ind + "<false/>\n")
		}
	default:
		return fmt.Errorf("profile: unsupported value %T", v)
	}
	return nil
}

func stableID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// uuidFrom derives a stable RFC 4122-shaped UUID (version 5 layout) from s.
func uuidFrom(s string) string {
	sum := sha256.Sum256([]byte(s))
	u := sum[:16]
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	h := strings.ToUpper(hex.EncodeToString(u))
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
