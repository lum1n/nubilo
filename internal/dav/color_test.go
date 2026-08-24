package dav

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeCalendarColor(t *testing.T) {
	cases := map[string]string{
		"#f00":      "#FF0000",
		"#FF0000":   "#FF0000",
		"0e61b9":    "#0E61B9",
		"#ff2d55ff": "#FF2D55FF",
		"#F00F":     "#FF0000FF",
		"nope":      "",
		"#12":       "",
		"":          "",
	}
	for in, want := range cases {
		if got := NormalizeCalendarColor(in); got != want {
			t.Fatalf("%q: %q want %q", in, got, want)
		}
	}
}

func TestInjectApplePropsForHref(t *testing.T) {
	body := `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
<D:response>
<D:href>/caldav/user/calendars/Personal</D:href>
<D:propstat>
<D:prop><D:displayname>Personal</D:displayname></D:prop>
<D:status>HTTP/1.1 200 OK</D:status>
</D:propstat>
<D:propstat>
<D:prop><calendar-color xmlns="http://apple.com/ns/ical/"></calendar-color></D:prop>
<D:status>HTTP/1.1 404 Not Found</D:status>
</D:propstat>
</D:response>
<D:response>
<D:href>/caldav/user/calendars/Personal/evt.ics</D:href>
<D:propstat>
<D:prop><D:getetag>"x"</D:getetag></D:prop>
<D:status>HTTP/1.1 200 OK</D:status>
</D:propstat>
</D:response>
</D:multistatus>`
	got := injectApplePropsForHref(body, "/caldav/user/calendars/Personal", CalendarColMeta{Color: "#FF0000", Order: 2})
	if !strings.Contains(got, "#FF0000") {
		t.Fatalf("missing color %s", got)
	}
	if !strings.Contains(got, ">2</") && !strings.Contains(got, "calendar-order") {
		t.Fatalf("missing order %s", got)
	}
	if strings.Count(got, "#FF0000") != 1 {
		t.Fatalf("color leaked into event href %s", got)
	}
	if strings.Contains(got, `xmlns="http://apple.com/ns/ical/"></calendar-color>`) {
		t.Fatalf("empty calendar-color left behind %s", got)
	}
}

func TestPatchCalendarColMetaKeepsComp(t *testing.T) {
	existing := json.RawMessage(`{"comp":"VTODO","order":3}`)
	got := ParseCalendarColMeta(PatchCalendarColMeta(existing, CalendarColMeta{Color: "#abc"}))
	if got.Color != "#AABBCC" || got.Comp != "VTODO" || got.Order != 3 {
		t.Fatalf("%+v", got)
	}
}
