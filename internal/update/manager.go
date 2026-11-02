package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Release is an available update, as the apps show it.
type Release struct {
	Version     string `json:"version"`
	Notes       string `json:"notes"`
	PublishedAt int64  `json:"published_at"`
}

// Outcome is how the last update ended.
type Outcome struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Result  string `json:"result"` // updated, rolled_back, failed
	Message string `json:"message,omitempty"`
	At      int64  `json:"at"`
}

// Status is GET /api/v1/admin/update.
type Status struct {
	Current    string   `json:"current"`
	Available  *Release `json:"available"`
	Auto       bool     `json:"auto"`
	State      string   `json:"state"` // idle, checking, downloading, preflight, installing, restarting
	Progress   float64  `json:"progress,omitempty"`
	Blocked    string   `json:"blocked"`
	CheckedAt  int64    `json:"checked_at"`
	NextAutoAt int64    `json:"next_auto_at"`
	Last       *Outcome `json:"last"`
	manifest   *Manifest
}

// Settings are kept in <data>/update/settings.json.
type Settings struct {
	Auto bool `json:"auto"`
}

// Manager checks for releases, installs them on request or at night, and
// reports status. One per server.
type Manager struct {
	DataDir string
	Current string
	Source  Source
	Install Installer
	// RelayProtocol is the wired protocol this binary speaks when the server
	// reaches the internet through a relay ("" without a relay). A release
	// speaking another one would cut the server off until the relay is
	// updated too, so it is not installed automatically.
	RelayProtocol string
	// Restart stops the server so the service manager starts the new
	// binary.
	Restart func()
	Log     *slog.Logger
	Now     func() time.Time
	// Window is the local hour automatic installs start in (default 3).
	WindowHour int

	mu     sync.Mutex
	status Status
	busy   bool
}

var ErrBusy = errors.New("update: an update is already under way")

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

func settingsPath(dataDir string) string { return filepath.Join(stateDir(dataDir), "settings.json") }

// LoadSettings returns the stored settings; automatic installs are on until
// an admin turns them off.
func LoadSettings(dataDir string) Settings {
	s := Settings{Auto: true}
	if b, err := os.ReadFile(settingsPath(dataDir)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// Status returns a copy of the current status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
	s := m.status
	return s
}

func (m *Manager) refreshLocked() {
	m.status.Current = m.Current
	m.status.Auto = LoadSettings(m.DataDir).Auto
	if m.status.State == "" {
		m.status.State = "idle"
	}
	if st, _ := LoadState(m.DataDir); st != nil {
		switch st.Phase {
		case PhaseDone:
			m.status.Last = &Outcome{From: st.From, To: st.To, Result: "updated", At: st.FinishedAt}
		case PhaseRolledBack:
			m.status.Last = &Outcome{From: st.From, To: st.To, Result: "rolled_back", Message: st.Message, At: st.FinishedAt}
		case PhaseFailed:
			m.status.Last = &Outcome{From: st.From, To: st.To, Result: "failed", Message: st.Message, At: st.FinishedAt}
		}
	}
	m.status.Available = nil
	m.status.Blocked = ""
	if man := m.status.manifest; man != nil && Compare(man.Version, m.Current) > 0 {
		m.status.Available = &Release{Version: man.Version, Notes: man.Notes, PublishedAt: man.PublishedAt}
		m.status.Blocked = m.blockedLocked(*man)
	}
	if m.status.Auto {
		m.status.NextAutoAt = m.nextWindow(m.now()).UnixMilli()
	} else {
		m.status.NextAutoAt = 0
	}
}

// blockedLocked says why man cannot be installed here, or "".
func (m *Manager) blockedLocked(man Manifest) string {
	exe := m.Install.Exe
	if strings.Contains(exe, ".app/Contents/") {
		return "This server runs inside the Nubilo app on this Mac. Update the app to update the server."
	}
	if _, ok := man.Artifact(runtime.GOOS, runtime.GOARCH); !ok {
		return fmt.Sprintf("Nubilo %s has no build for %s/%s.", man.Version, runtime.GOOS, runtime.GOARCH)
	}
	if man.MinFrom != "" && Compare(m.Current, man.MinFrom) < 0 {
		return fmt.Sprintf("Nubilo %s needs %s or later first. Update by hand, then updates install from here again.", man.Version, man.MinFrom)
	}
	if m.RelayProtocol != "" && man.WiredProtocol != "" && man.WiredProtocol != m.RelayProtocol {
		return fmt.Sprintf("Nubilo %s changes the relay protocol (%s to %s). Update the relay and this server together; see docs/RELAY.md, \"Upgrading wired\".",
			man.Version, m.RelayProtocol, man.WiredProtocol)
	}
	if err := writable(filepath.Dir(exe)); err != nil {
		return fmt.Sprintf("Nubilo cannot replace %s: %v", exe, err)
	}
	return ""
}

func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".nubilo-write-test-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

// SetAuto turns automatic nightly installs on or off.
func (m *Manager) SetAuto(auto bool) (Status, error) {
	if err := os.MkdirAll(stateDir(m.DataDir), 0o700); err != nil {
		return Status{}, err
	}
	b, _ := json.Marshal(Settings{Auto: auto})
	if err := writeAtomic(settingsPath(m.DataDir), b, 0o600); err != nil {
		return Status{}, err
	}
	return m.Status(), nil
}

// Check asks for the newest release.
func (m *Manager) Check(ctx context.Context) (Status, error) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return m.Status(), nil
	}
	m.status.State = "checking"
	m.mu.Unlock()

	man, err := m.Source.Latest(ctx)

	m.mu.Lock()
	m.status.State = "idle"
	m.status.CheckedAt = m.now().UnixMilli()
	switch {
	case errors.Is(err, ErrNoRelease):
		m.status.manifest = nil
		err = nil
	case err == nil:
		m.status.manifest = &man
	}
	m.mu.Unlock()
	if err != nil {
		m.log().Warn("update_check", "err", err.Error())
	}
	return m.Status(), err
}

// StartInstall installs the available release in the background and
// restarts the server when it is in place.
func (m *Manager) StartInstall() (Status, error) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return m.Status(), ErrBusy
	}
	m.refreshLocked()
	man := m.status.manifest
	if man == nil || m.status.Available == nil {
		m.mu.Unlock()
		return m.Status(), errors.New("update: no newer release; check first")
	}
	if why := m.status.Blocked; why != "" {
		m.mu.Unlock()
		return m.Status(), errors.New(why)
	}
	m.busy = true
	m.status.State = "downloading"
	m.status.Progress = 0
	m.mu.Unlock()

	go m.install(*man)
	return m.Status(), nil
}

func (m *Manager) install(man Manifest) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	fail := func(err error) {
		m.log().Error("update_install", "to", man.Version, "err", err.Error())
		// Install may have recorded more precisely what happened.
		if st, _ := LoadState(m.DataDir); st == nil || st.To != man.Version || st.Phase != PhaseFailed {
			_ = SaveState(m.DataDir, &State{
				Phase: PhaseFailed, From: m.Current, To: man.Version, Message: err.Error(),
				StartedAt: m.now().UnixMilli(), FinishedAt: m.now().UnixMilli(),
			})
		}
		m.mu.Lock()
		m.busy = false
		m.status.State, m.status.Progress = "idle", 0
		m.mu.Unlock()
	}
	a, _ := man.Artifact(runtime.GOOS, runtime.GOARCH)
	if err := os.MkdirAll(stateDir(m.DataDir), 0o700); err != nil {
		fail(err)
		return
	}
	bin, err := m.Source.Download(ctx, a, stateDir(m.DataDir), func(p float64) {
		m.mu.Lock()
		m.status.Progress = p
		m.mu.Unlock()
	})
	if err != nil {
		fail(err)
		return
	}
	defer os.Remove(bin)
	err = m.Install.Install(ctx, m.Current, man, bin, func(s Step) {
		m.mu.Lock()
		m.status.State, m.status.Progress = string(s), 0
		m.mu.Unlock()
	})
	if err != nil {
		fail(err)
		return
	}
	m.log().Info("update_installed", "from", m.Current, "to", man.Version)
	m.mu.Lock()
	m.status.State = "restarting"
	m.mu.Unlock()
	if m.Restart != nil {
		m.Restart()
	}
}

// nextWindow is the next automatic install time: WindowHour local time,
// plus a fixed per-server delay of up to two hours so servers don't all
// fetch a release at once.
func (m *Manager) nextWindow(now time.Time) time.Time {
	hour := m.WindowHour
	if hour == 0 {
		hour = 3
	}
	jitter := time.Duration(seedFor(m.DataDir)%120) * time.Minute
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location()).Add(jitter)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func seedFor(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// Run checks for releases about once a day and installs them in the
// nightly window when automatic installs are on. It returns when ctx ends.
func (m *Manager) Run(ctx context.Context) {
	// First check a little after start, then daily with some spread.
	wait := time.Minute + time.Duration(rand.IntN(60))*time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_, _ = m.Check(ctx)
		m.maybeAutoInstall()
		// Wake for the next window or the next daily check, whichever
		// comes first.
		wait = 24 * time.Hour
		if st := m.Status(); st.Auto {
			if until := time.Until(time.UnixMilli(st.NextAutoAt)); until > 0 && until < wait {
				wait = until
			}
		}
	}
}

func (m *Manager) maybeAutoInstall() {
	st := m.Status()
	if !st.Auto || st.Available == nil || st.Blocked != "" {
		return
	}
	// A release that already rolled back here waits for an admin.
	if st.Last != nil && st.Last.Result != "updated" && st.Last.To == st.Available.Version {
		return
	}
	now := m.now()
	start := m.nextWindow(now.Add(-3 * time.Hour))
	if now.Before(start) || now.After(start.Add(3*time.Hour)) {
		return
	}
	if _, err := m.StartInstall(); err != nil {
		m.log().Warn("update_auto", "err", err.Error())
	}
}
