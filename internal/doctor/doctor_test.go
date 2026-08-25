package doctor_test

import (
	"context"
	"strings"
	"testing"

	"nubilo/internal/doctor"
	"nubilo/internal/setup"
)

func TestServerDoctorAfterSetup(t *testing.T) {
	dir := t.TempDir()
	if _, err := setup.EnsureServerInitialized(dir, "127.0.0.1:18444"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setup.EnableAutoBackup(dir); err != nil {
		t.Fatal(err)
	}
	rep, err := doctor.Server(context.Background(), dir, doctor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Role != "server" {
		t.Fatalf("%+v", rep)
	}
	foundBak := false
	for _, c := range rep.Checks {
		if c.ID == "backup" && (c.Status == doctor.OK || c.Status == doctor.Warn) {
			foundBak = true
		}
		if c.ID == "master_key" && c.Status != doctor.OK {
			t.Fatalf("master_key: %+v", c)
		}
	}
	if !foundBak {
		t.Fatalf("backup check missing: %+v", rep.Checks)
	}
	out := doctor.FormatHuman(rep)
	if !strings.Contains(out, "nubilo doctor") {
		t.Fatalf("%s", out)
	}
}
