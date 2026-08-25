//go:build darwin

package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

func keychainAvailable() bool { return true }

func storeDeviceKeyPlatform(account string, key []byte) error {
	secret := keyBytesToSecret(key)
	// -U updates if present. -T "" restricts to this app by default after codesign;
	// for CLI we allow the current security context.
	cmd := exec.Command("security", "add-generic-password",
		"-a", account,
		"-s", deviceKeyService,
		"-w", secret,
		"-U",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("keychain store: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func loadDeviceKeyPlatform(account string) ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-a", account,
		"-s", deviceKeyService,
		"-w",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("keychain load: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return secretToKeyBytes(strings.TrimSpace(string(out)))
}
