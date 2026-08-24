package dav

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const appleICalNS = "http://apple.com/ns/ical/"

type CalendarColMeta struct {
	Color string `json:"color,omitempty"`
	Order int    `json:"order,omitempty"`
	Comp  string `json:"comp,omitempty"`
}

func ParseCalendarColMeta(raw json.RawMessage) CalendarColMeta {
	var m CalendarColMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	m.Color = NormalizeCalendarColor(m.Color)
	m.Comp = strings.ToUpper(strings.TrimSpace(m.Comp))
	return m
}

func EncodeCalendarColMeta(m CalendarColMeta) json.RawMessage {
	m.Color = NormalizeCalendarColor(m.Color)
	m.Comp = strings.ToUpper(strings.TrimSpace(m.Comp))
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func PatchCalendarColMeta(existing json.RawMessage, patch CalendarColMeta) json.RawMessage {
	m := ParseCalendarColMeta(existing)
	if c := NormalizeCalendarColor(patch.Color); c != "" {
		m.Color = c
	}
	if patch.Order != 0 {
		m.Order = patch.Order
	}
	if c := strings.ToUpper(strings.TrimSpace(patch.Comp)); c != "" {
		m.Comp = c
	}
	return EncodeCalendarColMeta(m)
}

// NormalizeCalendarColor accepts #RGB, #RGBA, #RRGGBB, or #RRGGBBAA.
func NormalizeCalendarColor(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r > unicode.MaxASCII {
			return ""
		}
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte(byte(r))
		case r >= 'a' && r <= 'f':
			b.WriteByte(byte(r - 'a' + 'A'))
		case r >= 'A' && r <= 'F':
			b.WriteByte(byte(r))
		default:
			return ""
		}
	}
	hex := b.String()
	switch len(hex) {
	case 3, 4:
		var out strings.Builder
		out.Grow(len(hex) * 2)
		for i := 0; i < len(hex); i++ {
			out.WriteByte(hex[i])
			out.WriteByte(hex[i])
		}
		hex = out.String()
	case 6, 8:
	default:
		return ""
	}
	return "#" + hex
}

func parseAppleCalPatch(body []byte) (color string, order int) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var inColor, inOrder bool
	var colorBuf, orderBuf strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "calendar-color":
				inColor = true
			case "calendar-order":
				inOrder = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "calendar-color":
				inColor = false
			case "calendar-order":
				inOrder = false
			}
		case xml.CharData:
			if inColor {
				colorBuf.Write(t)
			}
			if inOrder {
				orderBuf.Write(t)
			}
		}
	}
	color = NormalizeCalendarColor(colorBuf.String())
	order, _ = strconv.Atoi(strings.TrimSpace(orderBuf.String()))
	return color, order
}

func (b *CalDAV) serveCalendarPropPatch(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	color, order := parseAppleCalPatch(body)
	_, name, file, err := b.parseCalPath(r.URL.Path)
	if err == nil && file == "" && name != "" && (color != "" || order != 0) {
		if col, err := b.Engine.FindChildCollection(r.Context(), calKind, "", name); err == nil {
			if err := b.allow(r.Context(), true, col.ID); err != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			meta := PatchCalendarColMeta(col.Metadata, CalendarColMeta{Color: color, Order: order})
			_ = b.Engine.SetCollectionMetadata(r.Context(), col.ID, meta)
		}
	}
	writePropPatchOK(w, r.URL.Path, body)
}

func (h appleDAV) serveCalPropFind(w http.ResponseWriter, r *http.Request) {
	cw := &captureWriter{}
	h.next.ServeHTTP(cw, r)
	body := cw.buf.Bytes()
	code := cw.code
	if code == 0 {
		code = http.StatusOK
	}
	if h.cal != nil && (code == http.StatusMultiStatus || code == http.StatusOK) {
		body = h.cal.injectCalendarAppleProps(r.Context(), body)
	}
	for k, vs := range cw.h {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func (b *CalDAV) injectCalendarAppleProps(ctx context.Context, body []byte) []byte {
	if b == nil || b.Engine == nil || len(body) == 0 {
		return body
	}
	cols, err := b.Engine.ChildCollections(ctx, calKind, "")
	if err != nil || len(cols) == 0 {
		return body
	}
	s := string(body)
	for i := range cols {
		m := ParseCalendarColMeta(cols[i].Metadata)
		if m.Color == "" && m.Order == 0 {
			continue
		}
		href := Join(b.Prefix, calUserSeg, calHomeSeg, cols[i].Name)
		s = injectApplePropsForHref(s, href, m)
	}
	return []byte(s)
}

func injectApplePropsForHref(body, href string, m CalendarColMeta) string {
	href = strings.TrimSuffix(href, "/")
	needles := []string{
		href + "</",
		href + "/</",
		xmlEscape(href) + "</",
		xmlEscape(href) + "/</",
	}
	pos := -1
	for _, n := range needles {
		if i := strings.Index(body, n); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	if pos < 0 {
		return body
	}
	start, end, ok := responseBounds(body, pos)
	if !ok {
		return body
	}
	rewritten := rewriteCalendarPropBlock(body[start:end], m)
	return body[:start] + rewritten + body[end:]
}

func responseBounds(body string, hrefAt int) (start, end int, ok bool) {
	head := body[:hrefAt]
	start = lastIndexFold(head, "<d:response")
	if start < 0 {
		start = lastIndexFold(head, "<response")
	}
	if start < 0 {
		return 0, 0, false
	}
	rest := body[hrefAt:]
	rel := indexFold(rest, "</d:response>")
	n := len("</d:response>")
	if rel < 0 {
		rel = indexFold(rest, "</response>")
		n = len("</response>")
	}
	if rel < 0 {
		return 0, 0, false
	}
	return start, hrefAt + rel + n, true
}

func rewriteCalendarPropBlock(block string, m CalendarColMeta) string {
	var insert strings.Builder
	if m.Color != "" {
		insert.WriteString(`<calendar-color xmlns="` + appleICalNS + `">`)
		insert.WriteString(xmlEscape(m.Color))
		insert.WriteString(`</calendar-color>`)
	}
	if m.Order != 0 {
		insert.WriteString(`<calendar-order xmlns="` + appleICalNS + `">`)
		insert.WriteString(strconv.Itoa(m.Order))
		insert.WriteString(`</calendar-order>`)
	}
	if insert.Len() == 0 {
		return block
	}
	block = stripNamedElems(block, "calendar-color")
	block = stripNamedElems(block, "calendar-order")
	return injectIntoOKProp(block, insert.String())
}

var (
	calendarColorElemRe = namedElemRe("calendar-color")
	calendarOrderElemRe = namedElemRe("calendar-order")
)

func namedElemRe(local string) *regexp.Regexp {
	return regexp.MustCompile(`(?is)<([A-Za-z0-9._-]+:)?` + regexp.QuoteMeta(local) + `\b[^>]*/>|<([A-Za-z0-9._-]+:)?` + regexp.QuoteMeta(local) + `\b[^>]*>.*?</([A-Za-z0-9._-]+:)?` + regexp.QuoteMeta(local) + `\s*>`)
}

func stripNamedElems(s, local string) string {
	switch local {
	case "calendar-color":
		return calendarColorElemRe.ReplaceAllString(s, "")
	case "calendar-order":
		return calendarOrderElemRe.ReplaceAllString(s, "")
	default:
		return namedElemRe(local).ReplaceAllString(s, "")
	}
}

func injectIntoOKProp(block, insert string) string {
	i := indexFold(block, "HTTP/1.1 200")
	if i < 0 {
		return block + `<D:propstat><D:prop>` + insert + `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`
	}
	head := block[:i]
	closeAt := lastIndexFold(head, "</d:prop>")
	tagLen := len("</d:prop>")
	if closeAt < 0 {
		closeAt = lastIndexFold(head, "</prop>")
		tagLen = len("</prop>")
	}
	if closeAt < 0 {
		return block
	}
	return block[:closeAt] + insert + block[closeAt:closeAt+tagLen] + block[closeAt+tagLen:]
}

func indexFold(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}

func lastIndexFold(s, substr string) int {
	return strings.LastIndex(strings.ToLower(s), strings.ToLower(substr))
}

type captureWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (c *captureWriter) Header() http.Header {
	if c.h == nil {
		c.h = make(http.Header)
	}
	return c.h
}

func (c *captureWriter) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.buf.Write(p)
}
