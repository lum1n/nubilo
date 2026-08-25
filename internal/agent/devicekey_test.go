package agent_test

import (
	"path/filepath"
	"testing"

	"nubilo/internal/agent"
	ncrypto "nubilo/internal/crypto"
)

func TestStoreLoadDeviceKeyFile(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	key := ncrypto.PrivateKeyBytes(priv)
	if err := agent.StoreDeviceKey(dir, key); err != nil {
		t.Fatal(err)
	}
	got, err := agent.LoadDeviceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(key) {
		t.Fatal("mismatch")
	}
	src, err := agent.DeviceKeySource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if src != "file" && src != "keychain" {
		t.Fatalf("src=%s", src)
	}
	// Legacy file still loads.
	dir2 := t.TempDir()
	if err := ncrypto.WriteKeyFile(filepath.Join(dir2, "device.key"), key); err != nil {
		t.Fatal(err)
	}
	got2, err := agent.LoadDeviceKey(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != string(key) {
		t.Fatal("legacy mismatch")
	}
}
