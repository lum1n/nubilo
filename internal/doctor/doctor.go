package doctor

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nubilo/internal/agent"
	"nubilo/internal/app"
	"nubilo/internal/config"
	"nubilo/internal/identity"
	"nubilo/internal/integrity"
	"nubilo/internal/service"
)

// Severity for a check.
type Severity string

const (
	OK       Severity = "ok"
	Warn     Severity = "warn"
	Fail     Severity = "fail"
	Info     Severity = "info"
)

// Check is one health item.
type Check struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   Severity `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	Fix      string   `json:"fix,omitempty"`
}

// Report aggregates checks.
type Report struct {
	Role     string  `json:"role"` // server | agent
	DataDir  string  `json:"data_dir"`
	Checks   []Check `json:"checks"`
	OK       int     `json:"ok"`
	Warn     int     `json:"warn"`
	Fail     int     `json:"fail"`
	Healthy  bool    `json:"healthy"` // no fails
}

func (r *Report) add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Status {
	case OK:
		r.OK++
	case Warn:
		r.Warn++
	case Fail:
		r.Fail++
	}
	r.Healthy = r.Fail == 0
}

// Options controls optional expensive checks.
type Options struct {
	Verify bool // run integrity verify (slower)
}

// Server runs checks against an initialized server data dir.
func Server(ctx context.Context, dataDir string, opt Options) (Report, error) {
	r := Report{Role: "server", DataDir: dataDir, Healthy: true}
	p := config.Paths(dataDir)

	checkDirPerms(&r, dataDir)
	checkFilePerm(&r, "master_key", "master key", p.MasterKey, true)
	checkFilePerm(&r, "admin_token", "admin token", p.AdminToken, true)
	checkFilePerm(&r, "server_key", "server signing key", p.ServerKey, true)

	cfg, err := config.Load(p.Config)
	if err != nil {
		r.add(Check{ID: "config", Title: "config.json", Status: Fail, Detail: err.Error(), Fix: "nubilo init --data-dir " + dataDir})
		return r, nil
	}
	r.add(Check{ID: "config", Title: "config.json", Status: OK, Detail: "listen " + cfg.Listen})

	checkTLS(&r, cfg)
	checkBackup(&r, cfg)
	checkDiskEncryption(&r, dataDir)
	checkServerService(&r)

	rt, err := app.Open(dataDir)
	if err != nil {
		r.add(Check{ID: "runtime", Title: "open store", Status: Fail, Detail: err.Error()})
		return r, nil
	}
	defer rt.Close()

	devs, err := rt.IDs.List(ctx)
	if err != nil {
		r.add(Check{ID: "devices", Title: "devices", Status: Warn, Detail: err.Error()})
	} else {
		agents, davs := 0, 0
		for _, d := range devs {
			if d.Revoked() {
				continue
			}
			switch d.Role {
			case identity.RoleAgent:
				agents++
			case identity.RoleDAV:
				davs++
			}
		}
		st := OK
		detail := fmt.Sprintf("%d agent(s), %d dav password device(s)", agents, davs)
		fix := ""
		if agents == 0 {
			st = Warn
			fix = "nubilo pair --role agent  (then complete on the Mac)"
		}
		r.add(Check{ID: "devices", Title: "paired devices", Status: st, Detail: detail, Fix: fix})
	}

	if opt.Verify {
		issues, err := integrity.Check(ctx, rt.Store)
		if err != nil {
			r.add(Check{ID: "verify", Title: "integrity verify", Status: Fail, Detail: err.Error()})
		} else if len(issues) > 0 {
			r.add(Check{ID: "verify", Title: "integrity verify", Status: Fail,
				Detail: fmt.Sprintf("%d issue(s)", len(issues)),
				Fix:    "nubilo verify --repair",
			})
		} else {
			r.add(Check{ID: "verify", Title: "integrity verify", Status: OK, Detail: "clean"})
		}
	} else {
		r.add(Check{ID: "verify", Title: "integrity verify", Status: Info, Detail: "skipped (pass --verify)", Fix: "nubilo doctor --verify"})
	}

	return r, nil
}

// Agent runs checks against a Mac agent data dir.
func Agent(dataDir string) (Report, error) {
	r := Report{Role: "agent", DataDir: dataDir, Healthy: true}
	checkDirPerms(&r, dataDir)

	info, err := agent.ReadPairingInfo(dataDir)
	if err != nil {
		r.add(Check{ID: "paired", Title: "pairing", Status: Fail, Detail: err.Error()})
	} else if !info.Paired {
		r.add(Check{ID: "paired", Title: "pairing", Status: Fail, Detail: "not paired",
			Fix: "nubilo agent setup   or   nubilo pair --server URL --code … --name …"})
	} else {
		r.add(Check{ID: "paired", Title: "pairing", Status: OK,
			Detail: fmt.Sprintf("%s → %s", info.Name, info.Server)})
	}

	src, err := agent.DeviceKeySource(dataDir)
	if err != nil {
		st := Fail
		if !info.Paired {
			st = Warn
		}
		r.add(Check{ID: "device_key", Title: "device signing key", Status: st, Detail: err.Error(),
			Fix: "re-pair the agent"})
	} else if src == "keychain" {
		r.add(Check{ID: "device_key", Title: "device signing key", Status: OK, Detail: "macOS Keychain"})
	} else if src == "file" && runtime.GOOS == "darwin" {
		r.add(Check{ID: "device_key", Title: "device signing key", Status: Warn, Detail: "device.key (0600)",
			Fix: "key migrates to Keychain on next agent start"})
	} else {
		r.add(Check{ID: "device_key", Title: "device signing key", Status: OK, Detail: "device.key (0600)"})
	}

	paths := config.Paths(dataDir)
	sel, err := agent.LoadSelection(paths.AgentJSON)
	if err != nil {
		r.add(Check{ID: "selection", Title: "sync selection", Status: Warn, Detail: err.Error()})
	} else {
		n := len(sel.Calendars) + len(sel.Reminders)
		if sel.SyncContacts {
			n++
		}
		if sel.Photos.Enabled {
			n++
		}
		if sel.Files.Enabled {
			n++
		}
		if n == 0 {
			r.add(Check{ID: "selection", Title: "sync selection", Status: Warn, Detail: "nothing selected",
				Fix: "nubilo agent ui   or   nubilo agent select …"})
		} else {
			r.add(Check{ID: "selection", Title: "sync selection", Status: OK,
				Detail: fmt.Sprintf("calendars=%d reminders=%d contacts=%v photos=%v files=%v",
					len(sel.Calendars), len(sel.Reminders), sel.SyncContacts, sel.Photos.Enabled, sel.Files.Enabled)})
		}
	}

	if runtime.GOOS == "darwin" {
		st, err := service.Status(service.KindAgent)
		if err != nil {
			r.add(Check{ID: "service", Title: "LaunchAgent", Status: Warn, Detail: err.Error()})
		} else if !st.Loaded {
			r.add(Check{ID: "service", Title: "LaunchAgent", Status: Warn, Detail: "not installed/loaded",
				Fix: "nubilo agent install"})
		} else {
			r.add(Check{ID: "service", Title: "LaunchAgent", Status: OK, Detail: st.Detail})
		}
		auth := agent.PhotosAuthStatus()
		switch auth {
		case "authorized", "full":
			r.add(Check{ID: "photos_auth", Title: "Photos access", Status: OK, Detail: auth})
		case "limited":
			r.add(Check{ID: "photos_auth", Title: "Photos access", Status: Warn, Detail: "limited",
				Fix: "nubilo agent authorize  (Allow Full Access)"})
		case "denied", "notdetermined", "restricted":
			r.add(Check{ID: "photos_auth", Title: "Photos access", Status: Warn, Detail: auth,
				Fix: "nubilo agent authorize"})
		default:
			r.add(Check{ID: "photos_auth", Title: "Photos access", Status: Info, Detail: auth})
		}
	} else {
		r.add(Check{ID: "platform", Title: "platform", Status: Fail, Detail: "agent requires macOS"})
	}

	return r, nil
}

func checkDirPerms(r *Report, dir string) {
	fi, err := os.Stat(dir)
	if err != nil {
		r.add(Check{ID: "data_dir", Title: "data directory", Status: Fail, Detail: err.Error(), Fix: "nubilo init"})
		return
	}
	mode := fi.Mode().Perm()
	if mode&0o077 != 0 {
		r.add(Check{ID: "data_dir", Title: "data directory", Status: Fail,
			Detail: fmt.Sprintf("mode %04o (group/other readable)", mode),
			Fix:    "chmod 700 " + dir})
		return
	}
	r.add(Check{ID: "data_dir", Title: "data directory", Status: OK, Detail: fmt.Sprintf("%s mode %04o", dir, mode)})
}

func checkFilePerm(r *Report, id, title, path string, required bool) {
	fi, err := os.Stat(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return
		}
		r.add(Check{ID: id, Title: title, Status: Fail, Detail: err.Error()})
		return
	}
	mode := fi.Mode().Perm()
	if mode&0o077 != 0 {
		r.add(Check{ID: id, Title: title, Status: Fail,
			Detail: fmt.Sprintf("mode %04o", mode), Fix: "chmod 600 " + path})
		return
	}
	r.add(Check{ID: id, Title: title, Status: OK, Detail: fmt.Sprintf("mode %04o", mode)})
}

func checkTLS(r *Report, cfg config.Config) {
	certPath := cfg.TLS.Cert
	if certPath == "" {
		certPath = filepath.Join(cfg.DataDir, "tls.crt")
	}
	b, err := os.ReadFile(certPath)
	if err != nil {
		if cfg.Loopback() {
			r.add(Check{ID: "tls", Title: "TLS certificate", Status: Warn, Detail: "missing (loopback only)",
				Fix: "nubilo tls"})
		} else {
			r.add(Check{ID: "tls", Title: "TLS certificate", Status: Fail, Detail: err.Error(), Fix: "nubilo tls"})
		}
		return
	}
	block, _ := pem.Decode(b)
	if block == nil {
		r.add(Check{ID: "tls", Title: "TLS certificate", Status: Fail, Detail: "invalid PEM"})
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		r.add(Check{ID: "tls", Title: "TLS certificate", Status: Fail, Detail: err.Error()})
		return
	}
	until := time.Until(cert.NotAfter)
	detail := fmt.Sprintf("expires %s", cert.NotAfter.Format("2006-01-02"))
	if until < 0 {
		r.add(Check{ID: "tls", Title: "TLS certificate", Status: Fail, Detail: "expired", Fix: "nubilo tls"})
		return
	}
	if until < 30*24*time.Hour {
		r.add(Check{ID: "tls", Title: "TLS certificate", Status: Warn, Detail: detail, Fix: "nubilo tls"})
	} else {
		r.add(Check{ID: "tls", Title: "TLS certificate", Status: OK, Detail: detail})
	}
	if !cfg.Loopback() {
		r.add(Check{ID: "apple_tls", Title: "iPhone CalDAV/CardDAV TLS", Status: Warn,
			Detail: "Apple clients need a trusted cert (self-signed will fail)",
			Fix:    "put Tailscale Serve (or Caddy) in front, or install tls.crt on the phone"})
	}
}

func checkBackup(r *Report, cfg config.Config) {
	b := cfg.Backup
	if !b.Enabled {
		r.add(Check{ID: "backup", Title: "auto-backup", Status: Fail, Detail: "disabled",
			Fix: "nubilo setup   or enable config.backup + passphrase_file"})
		return
	}
	pf := b.PassphraseFile
	if pf == "" {
		r.add(Check{ID: "backup", Title: "auto-backup", Status: Fail, Detail: "enabled but no passphrase_file",
			Fix: "set backup.passphrase_file in config.json"})
		return
	}
	if _, err := os.Stat(pf); err != nil {
		r.add(Check{ID: "backup", Title: "auto-backup", Status: Fail, Detail: "passphrase file missing: " + pf,
			Fix: "create passphrase file mode 0600"})
		return
	}
	detail := fmt.Sprintf("every %dh, keep %d", b.IntervalHours, b.Keep)
	if b.LastBackupError != "" {
		fix := detail
		if strings.Contains(strings.ToLower(b.LastBackupError), "no space") {
			fix = "staging uses $data_dir/tmp (same volume as blobs), not /tmp — free space there, or prune old files in $data_dir/backups"
		}
		r.add(Check{ID: "backup", Title: "auto-backup", Status: Fail, Detail: b.LastBackupError, Fix: fix})
		return
	}
	if b.LastBackupUnixMS == 0 {
		r.add(Check{ID: "backup", Title: "auto-backup", Status: Warn, Detail: "configured, no successful backup yet",
			Fix: "start nubilo server (auto-backup runs in-process)"})
		return
	}
	age := time.Since(time.UnixMilli(b.LastBackupUnixMS))
	if age > time.Duration(b.IntervalHours*2)*time.Hour && b.IntervalHours > 0 {
		r.add(Check{ID: "backup", Title: "auto-backup", Status: Warn,
			Detail: fmt.Sprintf("last backup %s ago", age.Round(time.Hour)), Fix: detail})
		return
	}
	r.add(Check{ID: "backup", Title: "auto-backup", Status: OK,
		Detail: fmt.Sprintf("%s; last %s ago", detail, age.Round(time.Minute))})
}

func checkServerService(r *Report) {
	st, err := service.Status(service.KindServer)
	if err != nil {
		r.add(Check{ID: "service", Title: "always-on service", Status: Info, Detail: err.Error()})
		return
	}
	if !st.Installed && !st.Loaded {
		r.add(Check{ID: "service", Title: "always-on service", Status: Warn, Detail: "not installed",
			Fix: "nubilo server install"})
		return
	}
	if !st.Loaded {
		r.add(Check{ID: "service", Title: "always-on service", Status: Warn, Detail: "installed but not running",
			Fix: "nubilo server install"})
		return
	}
	r.add(Check{ID: "service", Title: "always-on service", Status: OK, Detail: st.Detail})
	if runtime.GOOS == "linux" {
		r.add(Check{ID: "linger", Title: "systemd linger", Status: Info,
			Detail: "user units stop on logout unless linger is enabled",
			Fix:    "loginctl enable-linger $USER"})
	}
}

func checkDiskEncryption(r *Report, dataDir string) {
	blobEnc := false
	if _, err := os.Stat(config.Paths(dataDir).MasterKey); err == nil {
		blobEnc = true
	}
	const appEncNote = "payloads (photos, files, calendars, contacts) are encrypted with the master key; SQLite metadata (names, sizes, IDs) and master.key are not — disk encryption covers those"
	const appEncFix = "optional: put data_dir on LUKS/FileVault so metadata and master.key are encrypted at rest too"

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("fdesetup", "status").CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil {
			if blobEnc {
				r.add(Check{ID: "disk_encryption", Title: "FileVault", Status: Warn,
					Detail: fmt.Sprintf("could not detect FileVault (%s); %s", s, appEncNote), Fix: appEncFix})
			} else {
				r.add(Check{ID: "disk_encryption", Title: "FileVault", Status: Fail, Detail: s,
					Fix: "enable FileVault, or initialize nubilo so blob encryption is active"})
			}
			return
		}
		if strings.Contains(strings.ToLower(s), "on") {
			r.add(Check{ID: "disk_encryption", Title: "FileVault", Status: OK, Detail: s})
			return
		}
		if blobEnc {
			r.add(Check{ID: "disk_encryption", Title: "FileVault", Status: Warn,
				Detail: fmt.Sprintf("%s; %s", s, appEncNote), Fix: appEncFix})
		} else {
			r.add(Check{ID: "disk_encryption", Title: "FileVault", Status: Fail, Detail: s,
				Fix: "enable FileVault (System Settings → Privacy & Security)"})
		}
	case "linux":
		abs, _ := filepath.Abs(dataDir)
		out, err := exec.Command("findmnt", "-n", "-o", "SOURCE,FSTYPE", "--target", abs).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil || s == "" {
			if blobEnc {
				r.add(Check{ID: "disk_encryption", Title: "disk encryption (LUKS)", Status: Warn,
					Detail: "could not detect mount source; " + appEncNote, Fix: appEncFix})
			} else {
				r.add(Check{ID: "disk_encryption", Title: "disk encryption (LUKS)", Status: Fail,
					Detail: "could not detect mount source and no master key (no blob encryption)",
					Fix:    "nubilo init / put data_dir on LUKS"})
			}
			return
		}
		low := strings.ToLower(s)
		if strings.Contains(low, "mapper") || strings.Contains(low, "crypt") || strings.Contains(low, "luks") {
			r.add(Check{ID: "disk_encryption", Title: "disk encryption (LUKS)", Status: OK, Detail: s})
			return
		}
		if blobEnc {
			r.add(Check{ID: "disk_encryption", Title: "disk encryption (LUKS)", Status: Warn,
				Detail: fmt.Sprintf("%s; %s", s, appEncNote), Fix: appEncFix})
		} else {
			r.add(Check{ID: "disk_encryption", Title: "disk encryption (LUKS)", Status: Fail,
				Detail: s + "; no master key found (no blob encryption either)",
				Fix:    "nubilo init, or move data_dir onto a LUKS volume"})
		}
	default:
		r.add(Check{ID: "disk_encryption", Title: "disk encryption", Status: Info, Detail: "unsupported OS check"})
	}
}

// FormatHuman prints a report for the terminal.
func FormatHuman(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "nubilo doctor (%s)  %s\n", r.Role, r.DataDir)
	for _, c := range r.Checks {
		mark := "·"
		switch c.Status {
		case OK:
			mark = "ok"
		case Warn:
			mark = "!!"
		case Fail:
			mark = "XX"
		case Info:
			mark = "--"
		}
		fmt.Fprintf(&b, "  [%s] %s", mark, c.Title)
		if c.Detail != "" {
			fmt.Fprintf(&b, " — %s", c.Detail)
		}
		b.WriteByte('\n')
		if c.Fix != "" && c.Status != OK && c.Status != Info {
			fmt.Fprintf(&b, "        fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(&b, "summary: %d ok, %d warn, %d fail\n", r.OK, r.Warn, r.Fail)
	if r.Healthy {
		b.WriteString("healthy: yes\n")
	} else {
		b.WriteString("healthy: NO — fix failures before trusting this deploy\n")
	}
	return b.String()
}
