package app

import (
	"context"
	"crypto/ed25519"
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
	defer st.Close()
	eng := syncengine.New(st)
	if _, err := eng.EnsureNamedCollection(context.Background(), "files", "Files"); err != nil {
		return err
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "calendar", "Personal"); err != nil {
		return err
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "addressbook", "Contacts"); err != nil {
		return err
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "photos", "Photos"); err != nil {
		return err
	}
	return nil
}

func Open(dataDir string) (*Runtime, error) {
	p := config.Paths(dataDir)
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
	if _, err := eng.EnsureNamedCollection(context.Background(), "files", "Files"); err != nil {
		st.Close()
		return nil, err
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "calendar", "Personal"); err != nil {
		st.Close()
		return nil, err
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "addressbook", "Contacts"); err != nil {
		st.Close()
		return nil, err
	}
	if _, err := eng.EnsureNamedCollection(context.Background(), "photos", "Photos"); err != nil {
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
