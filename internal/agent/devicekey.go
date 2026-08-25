package agent

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	ncrypto "nubilo/internal/crypto"
)

const deviceKeyService = "dev.nubilo.agent"

// DeviceKeyAccount is the Keychain account name for this data dir.
func DeviceKeyAccount(dataDir string) string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	sum := ncrypto.SHA256Hex([]byte(abs))
	return "device-key:" + sum[:16]
}

// StoreDeviceKey persists the Ed25519 private key. On macOS it prefers Keychain
// and removes a leftover device.key file after a successful store.
func StoreDeviceKey(dataDir string, key []byte) error {
	if len(key) != 64 {
		return fmt.Errorf("agent: device key: unexpected size %d", len(key))
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if keychainAvailable() {
		if err := storeDeviceKeyPlatform(DeviceKeyAccount(dataDir), key); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(dataDir, "device.key"))
		return nil
	}
	return ncrypto.WriteKeyFile(filepath.Join(dataDir, "device.key"), key)
}

// LoadDeviceKey reads the private key from Keychain (macOS) or device.key.
// If the key is only on disk on macOS, it is migrated into Keychain.
func LoadDeviceKey(dataDir string) ([]byte, error) {
	account := DeviceKeyAccount(dataDir)
	if keychainAvailable() {
		if b, err := loadDeviceKeyPlatform(account); err == nil && len(b) == 64 {
			return b, nil
		}
	}
	path := filepath.Join(dataDir, "device.key")
	b, err := ncrypto.ReadKeyFile(path, 64)
	if err != nil {
		if keychainAvailable() {
			return nil, fmt.Errorf("agent: device key not in Keychain or %s: %w", path, err)
		}
		return nil, err
	}
	if keychainAvailable() {
		if err := storeDeviceKeyPlatform(account, b); err == nil {
			_ = os.Remove(path)
		}
	}
	return b, nil
}

// DeviceKeySource reports where the key currently lives: "keychain", "file", or error.
func DeviceKeySource(dataDir string) (string, error) {
	account := DeviceKeyAccount(dataDir)
	if keychainAvailable() {
		if b, err := loadDeviceKeyPlatform(account); err == nil && len(b) == 64 {
			return "keychain", nil
		}
	}
	path := filepath.Join(dataDir, "device.key")
	if _, err := ncrypto.ReadKeyFile(path, 64); err == nil {
		return "file", nil
	} else {
		return "", err
	}
}

func keyBytesToSecret(key []byte) string {
	return hex.EncodeToString(key)
}

func secretToKeyBytes(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 64 {
		return nil, fmt.Errorf("agent: device key: unexpected size %d", len(b))
	}
	return b, nil
}
