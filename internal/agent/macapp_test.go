package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckBundleOwned(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Nubilo.app")
	if err := checkBundleOwned(app); err != nil {
		t.Fatalf("missing bundle: %v", err)
	}
	write := func(id string) {
		t.Helper()
		dir := filepath.Join(app, "Contents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		plist := "<dict>\n\t<key>CFBundleIdentifier</key>\n\t<string>" + id + "</string>\n</dict>\n"
		if err := os.WriteFile(filepath.Join(dir, "Info.plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(agentBundleID)
	if err := checkBundleOwned(app); err != nil {
		t.Fatalf("own bundle: %v", err)
	}
	write("dev.nubilo.app")
	if err := checkBundleOwned(app); err == nil || !strings.Contains(err.Error(), "dev.nubilo.app") {
		t.Fatalf("SwiftUI app: %v", err)
	}
}
