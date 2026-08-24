package dav_test

import (
	"net/http"
	"testing"
)

func TestAppPasswordFailureCooldown(t *testing.T) {
	ts, calDev, _, _, _, _ := calServer(t)
	var last int
	for i := 0; i < 8; i++ {
		req, _ := http.NewRequest("GET", ts.URL+"/caldav/user/", nil)
		req.SetBasicAuth(calDev.ID, "wrong-password")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		last = resp.StatusCode
		resp.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
}
