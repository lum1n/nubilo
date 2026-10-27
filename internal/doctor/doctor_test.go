package doctor_test

import (
	"context"
	"strings"
	"testing"

	"nubilo/internal/app"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/doctor"
	"nubilo/internal/identity"
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
	for _, c := range rep.Checks {
		if c.ID == "disk_encryption" && c.Status == doctor.Fail {
			t.Fatalf("initialized server should not fail disk_encryption when blobs are encrypted: %+v", c)
		}
	}
	out := doctor.FormatHuman(rep)
	if !strings.Contains(out, "nubilo doctor") {
		t.Fatalf("%s", out)
	}
}

func TestServerDoctorCountsAppDevices(t *testing.T) {
	dir := t.TempDir()
	if _, err := setup.EnsureServerInitialized(dir, "127.0.0.1:18445"); err != nil {
		t.Fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.IDs.Enroll(context.Background(), "", "iPhone", pub, identity.RoleClient); err != nil {
		t.Fatal(err)
	}
	rt.Close()
	rep, err := doctor.Server(context.Background(), dir, doctor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Checks {
		if c.ID == "devices" {
			if c.Status != doctor.OK || !strings.Contains(c.Detail, "1 app(s)") {
				t.Fatalf("devices: %+v", c)
			}
			return
		}
	}
	t.Fatalf("devices check missing: %+v", rep.Checks)
}
