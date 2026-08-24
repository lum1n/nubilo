package agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/protocol"
)

type pairingFile struct {
	DeviceID        string `json:"device_id"`
	Server          string `json:"server"`
	ServerPublicKey string `json:"server_public_key"`
	Name            string `json:"name"`
}

func LoadPairedClient(dataDir string, insecure bool) (*protocol.Client, error) {
	p := filepath.Join(dataDir, "device.json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotPaired
		}
		return nil, err
	}
	var f pairingFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.DeviceID == "" || f.Server == "" {
		return nil, ErrNotPaired
	}
	keyPath := filepath.Join(dataDir, "device.key")
	raw, err := ncrypto.ReadKeyFile(keyPath, 64)
	if err != nil {
		return nil, err
	}
	priv, err := ncrypto.ParsePrivateKey(raw)
	if err != nil {
		return nil, err
	}
	return protocol.NewClient(f.Server, f.DeviceID, priv, insecure), nil
}
