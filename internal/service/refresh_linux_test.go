//go:build linux

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshAddsGuardToGeneratedUnits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	reloads := 0
	daemonReload = func() error { reloads++; return nil }
	spec := Spec{Kind: KindServer, Exe: "/home/u/.local/bin/nubilo", DataDir: "/home/u/.nubilo"}
	current, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatal(err)
	}
	guard := "ExecStartPre=-/home/u/.local/bin/nubilo.prev update guard --data-dir /home/u/.nubilo\n"
	if !strings.Contains(current, guard) {
		t.Fatalf("no guard in\n%s", current)
	}
	path := filepath.Join(home, ".config", "systemd", "user", KindServer.UnitName())

	// Not installed: nothing to do.
	if changed, err := Refresh(spec); changed || err != nil {
		t.Fatalf("not installed: %v %v", changed, err)
	}
	// Written by an earlier nubilo: upgraded.
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(strings.Replace(current, guard, "", 1)), 0o644)
	if changed, err := Refresh(spec); !changed || err != nil || reloads != 1 {
		t.Fatalf("legacy unit: %v %v reloads=%d", changed, err, reloads)
	}
	if b, _ := os.ReadFile(path); string(b) != current {
		t.Fatal("unit not rewritten")
	}
	// Another server's unit (other data directory): not ours, no warning.
	other := spec
	other.DataDir = "/tmp/elsewhere"
	if changed, err := Refresh(other); changed || err != nil {
		t.Fatalf("other server: %v %v", changed, err)
	}
	// Already current.
	if changed, err := Refresh(spec); changed || err != nil {
		t.Fatalf("current: %v %v", changed, err)
	}
	// Edited by hand: left alone.
	edited := strings.Replace(current, "RestartSec=5", "RestartSec=30", 1)
	_ = os.WriteFile(path, []byte(edited), 0o644)
	if changed, err := Refresh(spec); changed || !errors.Is(err, ErrCustomized) {
		t.Fatalf("edited: %v %v", changed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != edited {
		t.Fatal("edited unit was changed")
	}
}
