package dav

import (
	"encoding/hex"
	"fmt"
	"net/http"

	ncrypto "nubilo/internal/crypto"
)

// LockCompat answers LOCK/UNLOCK so macOS Finder will write files.
// Tokens are not exclusive: this is a single-owner personal cloud.
func LockCompat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "LOCK":
			token := newLockToken()
			w.Header().Set("Lock-Token", "<"+token+">")
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(lockXML(r.URL.Path, token)))
			return
		case "UNLOCK":
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newLockToken() string {
	b, err := ncrypto.Random(16)
	if err != nil {
		return "urn:uuid:00000000-0000-0000-0000-000000000000"
	}
	h := hex.EncodeToString(b)
	return fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func lockXML(path, token string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock>
<D:locktype><D:write/></D:locktype>
<D:lockscope><D:exclusive/></D:lockscope>
<D:depth>infinity</D:depth>
<D:locktoken><D:href>` + xmlEscape(token) + `</D:href></D:locktoken>
<D:lockroot><D:href>` + xmlEscape(path) + `</D:href></D:lockroot>
</D:activelock></D:lockdiscovery></D:prop>`
}

func xmlEscape(s string) string {
	var buf []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			buf = append(buf, "&amp;"...)
		case '<':
			buf = append(buf, "&lt;"...)
		case '>':
			buf = append(buf, "&gt;"...)
		case '"':
			buf = append(buf, "&quot;"...)
		default:
			buf = append(buf, s[i])
		}
	}
	return string(buf)
}
