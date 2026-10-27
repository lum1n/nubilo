package app

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"nubilo/internal/audit"
	"nubilo/internal/auth"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/logging"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

type Runtime struct {
	Cfg        config.Config
	Paths      config.PathsSet
	Store      *store.Store
	IDs        *identity.Service
	Auth       *auth.Authenticator
	Engine     *syncengine.Engine
	Audit      *audit.Logger
	Log        *slog.Logger
	Master     []byte
	ServerPub  ed25519.PublicKey
	ServerPriv ed25519.PrivateKey
	AdminTok   []byte
}

func Init(dataDir string, listen string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	p := config.Paths(dataDir)
	if _, err := os.Stat(p.MasterKey); err == nil {
		return fmt.Errorf("already initialized: %s", p.MasterKey)
	}
	for _, d := range []string{p.Blobs, p.Tmp, p.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	master, err := ncrypto.GenerateMasterKey()
	if err != nil {
		return err
	}
	if err := ncrypto.WriteKeyFile(p.MasterKey, master); err != nil {
		return err
	}
	pub, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		return err
	}
	if err := ncrypto.WriteKeyFile(p.ServerKey, ncrypto.PrivateKeyBytes(priv)); err != nil {
		return err
	}
	_ = pub
	tok, err := ncrypto.Random(32)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.AdminToken, []byte(fmt.Sprintf("%x", tok)), 0o600); err != nil {
		return err
	}
	cfg := config.Defaults(dataDir)
	if listen != "" {
		cfg.Listen = listen
	}
	if _, err := EnsureAutoTLS(&cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(p.Config); err != nil {
		return err
	}
	blobKey, err := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	if err != nil {
		return err
	}
	st, err := store.Open(dataDir, p.DB, p.Blobs, p.Tmp, blobKey, cfg.Sync.MaxBlobBytes)
	if err != nil {
		return err
	}
	// Users (and their default collections) are created later: by redeeming
	// the setup invite, or lazily as the "owner" admin for CLI pairing.
	return st.Close()
}

// ErrNotInitialized means the data dir has no master key: `nubilo init`
// (or setup) has not run there.
var ErrNotInitialized = errors.New("server data dir not initialized")

func Open(dataDir string) (*Runtime, error) {
	p := config.Paths(dataDir)
	// Check before anything below writes config.json or a TLS certificate,
	// so a read-only command such as `status` never recreates a data dir.
	if _, err := os.Stat(p.MasterKey); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s (run nubilo init)", ErrNotInitialized, dataDir)
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		cfg = config.Defaults(dataDir)
	}
	cfg.DataDir = dataDir
	wasEmpty := cfg.TLS.Cert == "" || cfg.TLS.Key == ""
	created, err := EnsureAutoTLS(&cfg)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if created || (cfg.TLS.Auto && wasEmpty) {
		_ = cfg.Save(p.Config)
	}
	master, err := ncrypto.ReadKeyFile(p.MasterKey, ncrypto.MasterKeySize)
	if err != nil {
		return nil, fmt.Errorf("open master key: %w", err)
	}
	blobKey, err := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dataDir, p.DB, p.Blobs, p.Tmp, blobKey, cfg.Sync.MaxBlobBytes)
	if err != nil {
		return nil, err
	}
	privb, err := os.ReadFile(p.ServerKey)
	if err != nil {
		st.Close()
		return nil, err
	}
	priv, err := ncrypto.ParsePrivateKey(privb)
	if err != nil {
		st.Close()
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	tok, _ := os.ReadFile(p.AdminToken)
	log := logging.New(logging.Options{Level: cfg.Log.Level, SensitiveMetadata: cfg.Log.SensitiveMetadata})
	ids := identity.NewService(st)
	ids.TTL = time.Duration(cfg.Pairing.TTLSeconds) * time.Second
	ids.MaxAttempts = cfg.Pairing.MaxAttempts
	ids.MaxActive = cfg.Pairing.MaxActive
	a := &auth.Authenticator{
		IDs:      ids,
		Store:    st,
		SkewMS:   cfg.Sync.TimestampSkewMS,
		AdminTok: tok,
		MaxBody:  cfg.Sync.MaxBlobBytes,
	}
	eng := syncengine.New(st)
	ids.OnUserCreated = func(ctx context.Context, u *identity.User) error {
		return eng.EnsureUserDefaults(ctx, u.ID)
	}
	if err := ensureAllUserDefaults(context.Background(), ids, eng); err != nil {
		st.Close()
		return nil, err
	}
	al := &audit.Logger{Store: st, Slog: log}
	return &Runtime{
		Cfg:        cfg,
		Paths:      p,
		Store:      st,
		IDs:        ids,
		Auth:       a,
		Engine:     eng,
		Audit:      al,
		Log:        log,
		Master:     master,
		ServerPub:  pub,
		ServerPriv: priv,
		AdminTok:   tok,
	}, nil
}

// ensureAllUserDefaults makes sure every active user has the default
// collections (users created before the hook existed, or after deletions).
func ensureAllUserDefaults(ctx context.Context, ids *identity.Service, eng *syncengine.Engine) error {
	users, err := ids.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.Active() {
			continue
		}
		if err := eng.EnsureUserDefaults(ctx, u.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) Close() error {
	return r.Store.Close()
}

// EnsureAutoTLS writes a self-signed certificate covering localhost and
// local interface IPs when tls.auto is true and the files are missing.
func EnsureAutoTLS(cfg *config.Config) (created bool, err error) {
	if cfg == nil || !cfg.TLS.Auto {
		return false, nil
	}
	if cfg.DataDir == "" {
		return false, fmt.Errorf("tls: data_dir is required")
	}
	if cfg.TLS.Cert == "" {
		cfg.TLS.Cert = filepath.Join(cfg.DataDir, "tls.crt")
	}
	if cfg.TLS.Key == "" {
		cfg.TLS.Key = filepath.Join(cfg.DataDir, "tls.key")
	}
	_, err1 := os.Stat(cfg.TLS.Cert)
	_, err2 := os.Stat(cfg.TLS.Key)
	if err1 == nil && err2 == nil {
		return false, nil
	}
	if err := ncrypto.GenerateTLS(cfg.TLS.Cert, cfg.TLS.Key, ncrypto.LocalListenHosts(), 0); err != nil {
		return false, err
	}
	return true, nil
}

// RegenerateTLS always writes a new self-signed cert/key for local hosts and updates cfg paths.
func RegenerateTLS(cfg *config.Config, extraHosts []string) error {
	if cfg == nil || cfg.DataDir == "" {
		return fmt.Errorf("tls: data_dir is required")
	}
	cert := filepath.Join(cfg.DataDir, "tls.crt")
	key := filepath.Join(cfg.DataDir, "tls.key")
	hosts := ncrypto.LocalListenHosts()
	hosts = append(hosts, extraHosts...)
	if err := ncrypto.GenerateTLS(cert, key, hosts, 0); err != nil {
		return err
	}
	cfg.TLS.Cert = cert
	cfg.TLS.Key = key
	return nil
}

func ResolveDataDir(flag string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if v := os.Getenv("NUBILO_DATA_DIR"); v != "" {
		return filepath.Abs(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nubilo"), nil
}
