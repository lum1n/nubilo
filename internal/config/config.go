package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultListen = "127.0.0.1:8443"
	DefaultPort   = 8443
)

type Config struct {
	DataDir string       `json:"data_dir"`
	Listen  string       `json:"listen"`
	TLS     TLSConfig    `json:"tls"`
	Log     LogConfig    `json:"log"`
	Sync    SyncConfig   `json:"sync"`
	Pairing PairConfig   `json:"pairing"`
	HTTP    HTTPConfig   `json:"http"`
	Photos  PhotosConfig `json:"photos"`
}

type PhotosConfig struct {
	StripGPSFromDerivatives bool `json:"strip_gps_from_derivatives"`
	PerceptualHash          bool `json:"perceptual_hash"`
	ThumbMaxPx              int  `json:"thumb_max_px"`
	PreviewMaxPx            int  `json:"preview_max_px"`
}

type TLSConfig struct {
	Cert                  string `json:"cert"`
	Key                   string `json:"key"`
	AllowInsecureLoopback bool   `json:"allow_insecure_loopback"`
}

type LogConfig struct {
	Level             string `json:"level"`
	SensitiveMetadata bool   `json:"sensitive_metadata"`
}

type SyncConfig struct {
	MaxBatch        int   `json:"max_batch"`
	MaxBlobBytes    int64 `json:"max_blob_bytes"`
	TimestampSkewMS int64 `json:"timestamp_skew_ms"`
}

type PairConfig struct {
	TTLSeconds       int `json:"ttl_seconds"`
	MaxAttempts      int `json:"max_attempts"`
	MaxActive        int `json:"max_active"`
	BeginsPerHour    int `json:"begins_per_hour"`
	CompletesPerHour int `json:"completes_per_hour"`
}

type HTTPConfig struct {
	MaxHeaderBytes int `json:"max_header_bytes"`
}

func Defaults(dataDir string) Config {
	return Config{
		DataDir: dataDir,
		Listen:  DefaultListen,
		TLS: TLSConfig{
			AllowInsecureLoopback: true,
		},
		Log: LogConfig{
			Level: "info",
		},
		Sync: SyncConfig{
			MaxBatch:        500,
			MaxBlobBytes:    64 << 20,
			TimestampSkewMS: 60_000,
		},
		Pairing: PairConfig{
			TTLSeconds:       5 * 60,
			MaxAttempts:      5,
			MaxActive:        3,
			BeginsPerHour:    10,
			CompletesPerHour: 20,
		},
		HTTP: HTTPConfig{
			MaxHeaderBytes: 1 << 20,
		},
		Photos: PhotosConfig{
			StripGPSFromDerivatives: true,
			ThumbMaxPx:              256,
			PreviewMaxPx:            1280,
		},
	}
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Defaults("")
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

func applyEnv(c *Config) {
	if v := os.Getenv("NUBILO_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("NUBILO_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("NUBILO_TLS_CERT"); v != "" {
		c.TLS.Cert = v
	}
	if v := os.Getenv("NUBILO_TLS_KEY"); v != "" {
		c.TLS.Key = v
	}
	if v := os.Getenv("NUBILO_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
}

func (c Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("config: data_dir is required")
	}
	if c.Listen == "" {
		return fmt.Errorf("config: listen is required")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("config: listen: %w", err)
	}
	loopback := isLoopback(host)
	if !loopback && (c.TLS.Cert == "" || c.TLS.Key == "") {
		return fmt.Errorf("config: TLS cert and key are required for non-loopback listen %q", c.Listen)
	}
	if c.Sync.MaxBatch <= 0 {
		return fmt.Errorf("config: sync.max_batch must be positive")
	}
	if c.Sync.MaxBlobBytes <= 0 {
		return fmt.Errorf("config: sync.max_blob_bytes must be positive")
	}
	return nil
}

func (c Config) Loopback() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	return isLoopback(host)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func Paths(dataDir string) PathsSet {
	return PathsSet{
		Dir:        dataDir,
		Config:     filepath.Join(dataDir, "config.json"),
		MasterKey:  filepath.Join(dataDir, "master.key"),
		ServerKey:  filepath.Join(dataDir, "server.key"),
		AdminToken: filepath.Join(dataDir, "admin.token"),
		DB:         filepath.Join(dataDir, "metadata.db"),
		Blobs:      filepath.Join(dataDir, "blobs"),
		Tmp:        filepath.Join(dataDir, "tmp"),
		Logs:       filepath.Join(dataDir, "logs"),
		DeviceKey:  filepath.Join(dataDir, "device.key"),
		DeviceJSON: filepath.Join(dataDir, "device.json"),
		AgentJSON:  filepath.Join(dataDir, "agent.json"),
		AgentDB:    filepath.Join(dataDir, "agent.db"),
	}
}

type PathsSet struct {
	Dir, Config, MasterKey, ServerKey, AdminToken, DB, Blobs, Tmp, Logs, DeviceKey, DeviceJSON, AgentJSON, AgentDB string
}

func ParseListenOverride(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, ":") {
		p, err := strconv.Atoi(s)
		if err != nil {
			return "", fmt.Errorf("listen: %w", err)
		}
		return fmt.Sprintf("127.0.0.1:%d", p), nil
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return "", err
	}
	return s, nil
}
