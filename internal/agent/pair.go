package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/protocol"
)

// PairingInfo is the public view of device.json (no keys).
type PairingInfo struct {
	Paired   bool   `json:"paired"`
	DeviceID string `json:"device_id,omitempty"`
	Server   string `json:"server,omitempty"`
	Name     string `json:"name,omitempty"`
}

// ReadPairingInfo loads pairing metadata from dataDir/device.json.
func ReadPairingInfo(dataDir string) (PairingInfo, error) {
	p := filepath.Join(dataDir, "device.json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return PairingInfo{Paired: false}, nil
		}
		return PairingInfo{}, err
	}
	var f pairingFile
	if err := json.Unmarshal(b, &f); err != nil {
		return PairingInfo{}, err
	}
	if f.DeviceID == "" || f.Server == "" {
		return PairingInfo{Paired: false}, nil
	}
	return PairingInfo{
		Paired:   true,
		DeviceID: f.DeviceID,
		Server:   f.Server,
		Name:     f.Name,
	}, nil
}

// PairWithServer completes agent pairing and writes device.key + device.json.
func PairWithServer(dataDir, serverURL, code, name string, insecure bool) (deviceID string, err error) {
	if strings.TrimSpace(serverURL) == "" || strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("pair requires server, code, and name")
	}
	norm := ncrypto.NormalizePairingCode(code)
	if len(norm) != 10 || norm == "XXXXXXXXXX" {
		return "", fmt.Errorf("invalid pairing code (copy the code from the server; codes expire in 5 minutes)")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	pub, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		return "", err
	}
	var pinned string
	pol := protocol.TLS{
		Insecure: insecure,
		OnPeer: func(pem string, systemTrusted bool) {
			if !systemTrusted && pem != "" {
				pinned = pem
			}
		},
	}
	client := protocol.HTTPClient(30*time.Second, pol)
	base := strings.TrimRight(serverURL, "/")
	beginBody, _ := json.Marshal(map[string]string{
		"code":       code,
		"name":       name,
		"public_key": base64.RawStdEncoding.EncodeToString(pub),
	})
	resp, err := client.Post(base+"/api/v1/pair/begin", "application/json", strings.NewReader(string(beginBody)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(b))
		if resp.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("pair begin: %s %s (wrong or expired code)", resp.Status, msg)
		}
		return "", fmt.Errorf("pair begin: %s %s", resp.Status, msg)
	}
	var br struct {
		PairingID string `json:"pairing_id"`
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return "", err
	}
	chal, err := b64Decode(br.Challenge)
	if err != nil {
		return "", fmt.Errorf("challenge: %w", err)
	}
	sig := ncrypto.SignEd25519(priv, chal)
	compBody, _ := json.Marshal(map[string]string{
		"pairing_id": br.PairingID,
		"signature":  base64.RawStdEncoding.EncodeToString(sig),
	})
	resp2, err := client.Post(base+"/api/v1/pair/complete", "application/json", strings.NewReader(string(compBody)))
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		b, _ := io.ReadAll(resp2.Body)
		return "", fmt.Errorf("pair complete: %s %s", resp2.Status, strings.TrimSpace(string(b)))
	}
	var cr struct {
		DeviceID        string `json:"device_id"`
		ServerPublicKey string `json:"server_public_key"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&cr); err != nil {
		return "", err
	}
	keyPath := filepath.Join(dataDir, "device.key")
	if err := ncrypto.WriteKeyFile(keyPath, ncrypto.PrivateKeyBytes(priv)); err != nil {
		return "", err
	}
	out := pairingFile{
		DeviceID:        cr.DeviceID,
		Server:          serverURL,
		ServerPublicKey: cr.ServerPublicKey,
		Name:            name,
		ServerTLSCert:   pinned,
	}
	dj, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	jsonPath := filepath.Join(dataDir, "device.json")
	if err := os.WriteFile(jsonPath, append(dj, '\n'), 0o600); err != nil {
		return "", err
	}
	return cr.DeviceID, nil
}

func b64Decode(s string) ([]byte, error) {
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
