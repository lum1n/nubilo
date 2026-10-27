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
	Backup  BackupConfig `json:"backup"`
	Public  PublicConfig `json:"public"`
}

// PublicConfig describes how devices reach the server from outside the LAN:
// a public domain with an ACME (Let's Encrypt) certificate, served either
// through a wired relay on a VPS or directly on public ports.
type PublicConfig struct {
	// ServerName is shown to users (app, profiles). Defaults to "Nubilo".
	ServerName string `json:"server_name,omitempty"`
	// Domain is the public DNS name, e.g. cloud.example.com.
	Domain string      `json:"domain,omitempty"`
	ACME   ACMEConfig  `json:"acme"`
	Relay  RelayConfig `json:"relay"`
	// DirectListen is the public HTTPS address (e.g. ":443") when the host is
	// reachable directly. DirectHTTPListen (default ":80") serves ACME HTTP-01
	// and redirects to HTTPS.
	DirectListen     string `json:"direct_listen,omitempty"`
	DirectHTTPListen string `json:"direct_http_listen,omitempty"`
	// Tailscale serves the tailnet's MagicDNS name with a certificate from
	// `tailscale cert`. It is the device URL when Domain is empty.
	Tailscale TailscaleConfig `json:"tailscale"`
}

// TailscaleConfig: devices on the tailnet reach the LAN listener by its
// MagicDNS name, e.g. https://box.tail1234.ts.net:8443.
type TailscaleConfig struct {
	Enabled bool `json:"enabled"`
	// Name is the MagicDNS name, without the trailing dot.
	Name string `json:"name,omitempty"`
	// Port in device URLs; 0 means the port of Listen. Set 443 when Tailscale
	// Funnel forwards public TCP 443 to the listener.
	Port int `json:"port,omitempty"`
}

type ACMEConfig struct {
	Enabled bool   `json:"enabled"`
	Email   string `json:"email,omitempty"`
	// Directory overrides the ACME directory URL (e.g. Let's Encrypt staging).
	Directory string `json:"directory,omitempty"`
}

// RelayConfig joins a wired overlay whose relay forwards public 443/80 here.
type RelayConfig struct {
	Invite   string `json:"invite,omitempty"`
	Name     string `json:"name,omitempty"`
	StateDir string `json:"state_dir,omitempty"`
	// BehindProxy: the relay sits behind a TLS-terminating proxy (exe.dev)
	// that holds the certificate for Domain. The home server serves plain
	// HTTP to the relay over the overlay and runs no ACME.
	BehindProxy bool `json:"behind_proxy,omitempty"`
}

// LetsEncryptStaging is the staging ACME directory for dry runs.
const LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

// Name returns the display name of this server.
func (p PublicConfig) Name() string {
	if strings.TrimSpace(p.ServerName) != "" {
		return strings.TrimSpace(p.ServerName)
	}
	return "Nubilo"
}

// BaseURL returns the public https URL, or "" when no domain is set.
func (p PublicConfig) BaseURL() string {
	d := strings.TrimSpace(p.Domain)
	if d == "" {
		return ""
	}
	return "https://" + d
}

type PhotosConfig struct {
	StripGPSFromDerivatives bool `json:"strip_gps_from_derivatives"`
	PerceptualHash          bool `json:"perceptual_hash"`
	ThumbMaxPx              int  `json:"thumb_max_px"`
	PreviewMaxPx            int  `json:"preview_max_px"`
}

// BackupConfig controls optional automatic encrypted backups (server process).
// Passphrase is never stored in config; only a path to a passphrase file on disk.
type BackupConfig struct {
	Enabled          bool   `json:"enabled"`
	IntervalHours    int    `json:"interval_hours"`
	PassphraseFile   string `json:"passphrase_file"`
	Keep             int    `json:"keep"`
	LastBackupUnixMS int64  `json:"last_backup_unix_ms,omitempty"`
	LastBackupError  string `json:"last_backup_error,omitempty"`
}

type TLSConfig struct {
	Cert                  string `json:"cert"`
	Key                   string `json:"key"`
	Auto                  bool   `json:"auto"`
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

// DefaultMaxBlobBytes is large enough for typical phone videos (old defaults
// of 32/64 MiB rejected many MP4s in the 33–60 MiB range).
const DefaultMaxBlobBytes int64 = 256 << 20
const legacyMaxBlobBytes32 int64 = 32 << 20
const legacyMaxBlobBytes64 int64 = 64 << 20

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
			Auto:                  true,
			AllowInsecureLoopback: true,
		},
		Log: LogConfig{
			Level: "info",
		},
		Sync: SyncConfig{
			MaxBatch:        500,
			MaxBlobBytes:    DefaultMaxBlobBytes,
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
		Backup: BackupConfig{
			Enabled:       false,
			IntervalHours: 24,
			Keep:          7,
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
	// Phone videos commonly exceed the old 32/64 MiB defaults; bump only those
	// exact legacy values so intentional smaller caps stay put.
	switch cfg.Sync.MaxBlobBytes {
	case legacyMaxBlobBytes32, legacyMaxBlobBytes64:
		cfg.Sync.MaxBlobBytes = DefaultMaxBlobBytes
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
	if !loopback && !c.TLS.Auto && (c.TLS.Cert == "" || c.TLS.Key == "") {
		return fmt.Errorf("config: TLS cert and key are required for non-loopback listen %q (or leave tls.auto=true)", c.Listen)
	}
	if c.Sync.MaxBatch <= 0 {
		return fmt.Errorf("config: sync.max_batch must be positive")
	}
	if c.Sync.MaxBlobBytes <= 0 {
		return fmt.Errorf("config: sync.max_blob_bytes must be positive")
	}
	if d := c.Public.Domain; d != "" {
		if strings.ContainsAny(d, "/: ") || !strings.Contains(d, ".") {
			return fmt.Errorf("config: public.domain %q must be a bare DNS name", d)
		}
	}
	if c.Public.ACME.Enabled && c.Public.Domain == "" {
		return fmt.Errorf("config: public.acme requires public.domain")
	}
	if ts := c.Public.Tailscale; ts.Enabled {
		if ts.Name == "" || strings.ContainsAny(ts.Name, "/: ") || !strings.HasSuffix(ts.Name, ".ts.net") {
			return fmt.Errorf("config: public.tailscale.name %q must be the MagicDNS name (….ts.net)", ts.Name)
		}
		if ts.Port < 0 || ts.Port > 65535 {
			return fmt.Errorf("config: public.tailscale.port %d out of range", ts.Port)
		}
	}
	if c.Public.DirectListen != "" {
		if _, _, err := net.SplitHostPort(c.Public.DirectListen); err != nil {
			return fmt.Errorf("config: public.direct_listen: %w", err)
		}
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
		ACME:       filepath.Join(dataDir, "acme"),
		Wired:      filepath.Join(dataDir, "wired"),
		Tailscale:  filepath.Join(dataDir, "tailscale"),
	}
}

type PathsSet struct {
	Dir, Config, MasterKey, ServerKey, AdminToken, DB, Blobs, Tmp, Logs, DeviceKey, DeviceJSON, AgentJSON, AgentDB string
	ACME, Wired, Tailscale                                                                                         string
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

// PublicHostPort is the name and port devices use away from the LAN: the
// public domain on 443, else the Tailscale name. ok is false when neither
// is configured.
func (c Config) PublicHostPort() (host string, port int, ok bool) {
	if d := strings.TrimSpace(c.Public.Domain); d != "" {
		return d, 443, true
	}
	ts := c.Public.Tailscale
	if !ts.Enabled || ts.Name == "" {
		return "", 0, false
	}
	port = ts.Port
	if port == 0 {
		_, p, err := net.SplitHostPort(c.Listen)
		if err != nil {
			return "", 0, false
		}
		port, _ = strconv.Atoi(p)
	}
	return ts.Name, port, true
}

// PublicURL is the https URL of PublicHostPort, or "" when there is none.
func (c Config) PublicURL() string {
	host, port, ok := c.PublicHostPort()
	if !ok {
		return ""
	}
	if port == 443 {
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// DeviceBaseURL is the URL other devices should use: PublicURL when set,
// else the listen address with a wildcard host replaced by a LAN IP.
func (c Config) DeviceBaseURL() string {
	if u := c.PublicURL(); u != "" {
		return u
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return "https://" + c.Listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = lanIP()
	}
	scheme := "https://"
	if isLoopback(host) && c.TLS.Cert == "" {
		scheme = "http://"
	}
	return scheme + net.JoinHostPort(host, port)
}

// lanIP returns the first private IPv4 address of this host, or 127.0.0.1.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}
