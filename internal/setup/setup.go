package setup

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nubilo/internal/app"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/service"
)

// EnableAutoBackup writes a passphrase file and turns on config.backup.
// Returns the passphrase (show once) and the passphrase file path.
func EnableAutoBackup(dataDir string) (passphrase, passFile string, err error) {
	p := config.Paths(dataDir)
	cfg, err := config.Load(p.Config)
	if err != nil {
		return "", "", err
	}
	passFile = filepath.Join(dataDir, "backup.passphrase")
	if cfg.Backup.PassphraseFile != "" {
		passFile = cfg.Backup.PassphraseFile
	}
	if _, err := os.Stat(passFile); err == nil {
		// Already have a passphrase file — just ensure backup is enabled.
		cfg.Backup.Enabled = true
		if cfg.Backup.IntervalHours <= 0 {
			cfg.Backup.IntervalHours = 24
		}
		if cfg.Backup.Keep <= 0 {
			cfg.Backup.Keep = 7
		}
		cfg.Backup.PassphraseFile = passFile
		if err := cfg.Save(p.Config); err != nil {
			return "", "", err
		}
		return "", passFile, nil
	}
	raw, err := ncrypto.Random(24)
	if err != nil {
		return "", "", err
	}
	passphrase = base64.RawURLEncoding.EncodeToString(raw)
	if err := os.WriteFile(passFile, []byte(passphrase+"\n"), 0o600); err != nil {
		return "", "", err
	}
	cfg.Backup.Enabled = true
	cfg.Backup.IntervalHours = 24
	cfg.Backup.Keep = 7
	cfg.Backup.PassphraseFile = passFile
	if err := cfg.Save(p.Config); err != nil {
		return "", "", err
	}
	return passphrase, passFile, nil
}

// EnsureServerInitialized runs init if needed.
func EnsureServerInitialized(dataDir, listen string) (created bool, err error) {
	p := config.Paths(dataDir)
	if _, err := os.Stat(p.MasterKey); err == nil {
		return false, nil
	}
	if listen == "" {
		listen = "0.0.0.0:8443"
	}
	if err := app.Init(dataDir, listen); err != nil {
		return false, err
	}
	return true, nil
}

// InstallServerService registers the always-on server unit.
func InstallServerService(dataDir string) (unitPath string, err error) {
	exe, err := service.ResolveExe()
	if err != nil {
		return "", err
	}
	return service.Install(service.Spec{
		Kind:    service.KindServer,
		Exe:     exe,
		DataDir: dataDir,
	})
}

// InstallAgentService copies into Nubilo.app (darwin) and loads LaunchAgent.
func InstallAgentService(dataDir string, insecure bool) (unitPath string, err error) {
	exe, err := service.ResolveExe()
	if err != nil {
		return "", err
	}
	if appPath, err := agentAppBundle(); err == nil && appPath != "" {
		exe = appPath
	}
	return service.Install(service.Spec{
		Kind:     service.KindAgent,
		Exe:      exe,
		DataDir:  dataDir,
		Insecure: insecure,
	})
}

// RandomBackupPassphrase is exported for tests.
func RandomBackupPassphrase() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NextStepsServer returns operator hints after setup.
func NextStepsServer(dataDir string) string {
	var b strings.Builder
	b.WriteString("Next steps:\n")
	b.WriteString("  1. Keep the server running: nubilo server install\n")
	b.WriteString("  2. On the Mac: nubilo agent setup --data-dir ~/.nubilo-agent\n")
	b.WriteString("  3. For iPhone Calendar/Contacts: Tailscale Serve (or install tls.crt)\n")
	b.WriteString("  4. Issue DAV passwords: nubilo devices password --scope caldav|carddav|webdav\n")
	b.WriteString("  5. Re-check: nubilo doctor --data-dir " + dataDir + "\n")
	b.WriteString("Store the backup passphrase offline. Losing it means backups are unrecoverable.\n")
	return b.String()
}

// PrintPassphraseOnce formats the one-time passphrase disclosure.
func PrintPassphraseOnce(pass, passFile string) string {
	if pass == "" {
		return fmt.Sprintf("auto-backup enabled (existing passphrase file %s)\n", passFile)
	}
	return fmt.Sprintf(`auto-backup enabled
  passphrase file: %s
  passphrase (SAVE THIS — shown once):

    %s

`, passFile, pass)
}
