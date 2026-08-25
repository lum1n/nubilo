//go:build !darwin

package agent

import "fmt"

func keychainAvailable() bool { return false }

func storeDeviceKeyPlatform(account string, key []byte) error {
	return fmt.Errorf("keychain unavailable")
}

func loadDeviceKeyPlatform(account string) ([]byte, error) {
	return nil, fmt.Errorf("keychain unavailable")
}
