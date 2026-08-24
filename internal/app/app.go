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
	if err := cfg.Validate(); err != nil {
		return nil, err
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
