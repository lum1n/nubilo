package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nubilo/internal/backup"
	"nubilo/internal/config"
)

// AutoBackup runs rotating encrypted backups while the server process is alive.
type AutoBackup struct {
	RT   *Runtime
	Log  *slog.Logger
	stop chan struct{}
	wg   sync.WaitGroup
}

func (a *AutoBackup) Start() {
	if a == nil || a.RT == nil {
		return
	}
	if a.Log == nil {
		a.Log = slog.Default()
	}
	a.stop = make(chan struct{})
	a.wg.Add(1)
	go a.loop()
}

func (a *AutoBackup) Stop() {
	if a == nil || a.stop == nil {
		return
	}
	close(a.stop)
	a.wg.Wait()
}

func (a *AutoBackup) loop() {
	defer a.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	a.maybeRun()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.maybeRun()
		}
	}
}

func (a *AutoBackup) maybeRun() {
	cfgPath := config.Paths(a.RT.Cfg.DataDir).Config
	cfg, err := config.Load(cfgPath)
	if err != nil {
		cfg = a.RT.Cfg
	}
	if !cfg.Backup.Enabled {
		return
	}
	hours := cfg.Backup.IntervalHours
	if hours <= 0 {
		hours = 24
	}
	interval := time.Duration(hours) * time.Hour
	if cfg.Backup.LastBackupUnixMS > 0 {
		last := time.UnixMilli(cfg.Backup.LastBackupUnixMS)
		if time.Since(last) < interval {
			return
		}
	}
	pf := strings.TrimSpace(cfg.Backup.PassphraseFile)
	if pf == "" {
		a.recordErr(&cfg, "backup.passphrase_file empty")
		return
	}
	if !filepath.IsAbs(pf) {
		pf = filepath.Join(a.RT.Cfg.DataDir, pf)
	}
	raw, err := os.ReadFile(pf)
	if err != nil {
		a.recordErr(&cfg, err.Error())
		return
	}
	phrase := strings.TrimSpace(string(raw))
	if phrase == "" {
		a.recordErr(&cfg, "empty passphrase file")
		return
	}
	keep := cfg.Backup.Keep
	path, err := backup.RotateCreate(context.Background(), a.RT.Store, a.RT.Cfg.DataDir, phrase, keep)
	if err != nil {
		a.recordErr(&cfg, err.Error())
		return
	}
	cfg.Backup.LastBackupUnixMS = time.Now().UnixMilli()
	cfg.Backup.LastBackupError = ""
	cfg.DataDir = a.RT.Cfg.DataDir
	if err := cfg.Save(cfgPath); err != nil {
		a.Log.Warn("autobackup_save", "err", err.Error())
	}
	a.RT.Cfg.Backup = cfg.Backup
	a.Log.Info("autobackup_ok", "path", path)
}

func (a *AutoBackup) recordErr(cfg *config.Config, msg string) {
	a.Log.Warn("autobackup", "err", msg)
	cfg.Backup.LastBackupError = msg
	cfg.DataDir = a.RT.Cfg.DataDir
	_ = cfg.Save(config.Paths(a.RT.Cfg.DataDir).Config)
	a.RT.Cfg.Backup = cfg.Backup
}
