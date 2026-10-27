package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// agentBundleID identifies the TCC-only bundle `nubilo agent install`
// writes. The SwiftUI app is dev.nubilo.app and shares the name Nubilo.app.
const agentBundleID = "dev.nubilo.cli"

var bundleIDRE = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]*)</string>`)

// checkBundleOwned refuses to overwrite an app bundle at appPath that is
// not the agent's own, such as the Nubilo app dragged into ~/Applications.
func checkBundleOwned(appPath string) error {
	b, err := os.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	m := bundleIDRE.FindSubmatch(b)
	if m == nil || string(m[1]) == agentBundleID {
		return nil
	}
	return fmt.Errorf("%s is another app (%s); move it to /Applications, then run nubilo agent install again", appPath, m[1])
}
