//go:build darwin

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const nubiloAppName = "Nubilo.app"

// AppBundlePath is ~/Applications/Nubilo.app
func AppBundlePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", nubiloAppName), nil
}

// InstallAppBundle copies this executable into ~/Applications/Nubilo.app and ad-hoc signs it
// so TCC can show a Photos permission dialog attributed to Nubilo (not Terminal).
func InstallAppBundle() (appPath string, err error) {
	appPath, err = AppBundlePath()
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	macOS := filepath.Join(appPath, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(macOS, "nubilo")
	in, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, in, 0o755); err != nil {
		return "", err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>nubilo</string>
	<key>CFBundleIdentifier</key>
	<string>dev.nubilo.cli</string>
	<key>CFBundleName</key>
	<string>Nubilo</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSPhotoLibraryUsageDescription</key>
	<string>Nubilo syncs selected albums and photos with your personal cloud.</string>
	<key>NSPhotoLibraryAddUsageDescription</key>
	<string>Nubilo can write photos from your cloud back into Photos.</string>
	<key>NSCalendarsUsageDescription</key>
	<string>Nubilo syncs selected calendars with your personal cloud.</string>
	<key>NSCalendarsFullAccessUsageDescription</key>
	<string>Nubilo syncs selected calendars with your personal cloud.</string>
	<key>NSRemindersUsageDescription</key>
	<string>Nubilo syncs selected reminder lists with your personal cloud.</string>
	<key>NSRemindersFullAccessUsageDescription</key>
	<string>Nubilo syncs selected reminder lists with your personal cloud.</string>
	<key>NSContactsUsageDescription</key>
	<string>Nubilo syncs contacts with your personal cloud.</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		return "", err
	}
	ent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.security.personal-information.photos-library</key>
	<true/>
	<key>com.apple.security.personal-information.calendars</key>
	<true/>
	<key>com.apple.security.personal-information.addressbook</key>
	<true/>
</dict>
</plist>
`
	entPath := filepath.Join(appPath, "Contents", "nubilo.entitlements")
	if err := os.WriteFile(entPath, []byte(ent), 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command("codesign", "--force", "--deep", "--sign", "-", "--entitlements", entPath, appPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("codesign: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return appPath, nil
}

// AuthorizePhotosViaApp launches Nubilo.app so the system Photos dialog is attributed to Nubilo.
func AuthorizePhotosViaApp() (status string, err error) {
	appPath, err := InstallAppBundle()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("open", "-W", "-n", appPath, "--args", "--authorize-photos")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("open app: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return PhotosAuthStatus(), nil
}

// OpenPhotosPrivacySettings opens System Settings to the Photos privacy pane.
func OpenPhotosPrivacySettings() error {
	urls := []string{
		"x-apple.systempreferences:com.apple.preference.security?Privacy_Photos",
		"x-apple.systempreferences:com.apple.Settings.PrivacySecurity.extension?Privacy_Photos",
	}
	var last error
	for _, u := range urls {
		if err := exec.Command("open", u).Run(); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return last
}
