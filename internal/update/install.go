package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Phase is where an update stands, kept in <data>/update/state.json across
// the restart.
type Phase string

const (
	// The binary was swapped; the new one has not checked in yet.
	PhaseRestarting Phase = "restarting"
	// The new binary started and is checking that it is healthy.
	PhaseVerifying Phase = "verifying"
	// The new binary gave up; the next start restores the backup.
	PhaseRollback   Phase = "rollback"
	PhaseDone       Phase = "done"
	PhaseRolledBack Phase = "rolled_back"
	// The install stopped before anything was replaced.
	PhaseFailed Phase = "failed"
)

// State is the update in progress, or the last one.
type State struct {
	Phase     Phase  `json:"phase"`
	From      string `json:"from"`
	To        string `json:"to"`
	Exe       string `json:"exe"`
	Backup    string `json:"backup"`
	WiredDir  string `json:"wired_dir,omitempty"`
	StartedAt int64  `json:"started_at"`
	// Starts of the new binary so far.
	Attempts     int    `json:"attempts"`
	SchemaBefore int    `json:"schema_before"`
	Message      string `json:"message,omitempty"`
	FinishedAt   int64  `json:"finished_at,omitempty"`
}

// Limits for a new binary to prove itself before it is rolled back.
const (
	maxAttempts   = 3
	startDeadline = 3 * time.Minute
	// HealthyWithin is how long a started server has to report healthy.
	HealthyWithin = 2 * time.Minute
	keepBackups   = 3
)

func stateDir(dataDir string) string  { return filepath.Join(dataDir, "update") }
func statePath(dataDir string) string { return filepath.Join(stateDir(dataDir), "state.json") }

// LoadState returns the recorded update, or nil.
func LoadState(dataDir string) (*State, error) {
	b, err := os.ReadFile(statePath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("update: %s: %w", statePath(dataDir), err)
	}
	return &s, nil
}

// SaveState writes s atomically.
func SaveState(dataDir string, s *State) error {
	if err := os.MkdirAll(stateDir(dataDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(statePath(dataDir), b, 0o600)
}

// Installer puts a downloaded release in place of the running binary.
type Installer struct {
	DataDir  string
	Exe      string // the binary the service runs
	WiredDir string // wired state, backed up with the database ("" = none)
	// Snapshot writes a consistent copy of the live database to path
	// (VACUUM INTO on the open store). Nil for a relay, which has none: the
	// new binary is then only checked to run on this host.
	Snapshot func(ctx context.Context, path string) error
	// Run runs a binary; tests replace it.
	Run func(ctx context.Context, bin string, args ...string) error
	Now func() time.Time
}

func (in Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in Installer) run(ctx context.Context, bin string, args ...string) error {
	if in.Run != nil {
		return in.Run(ctx, bin, args...)
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

// Step names the part of an install in progress, for the status.
type Step string

const (
	StepPreflight  Step = "preflight"
	StepInstalling Step = "installing"
)

// Install checks newBin against a copy of the data, backs up, and swaps it
// in. On success the caller restarts the service; the new binary finishes
// the update when it reports healthy (Boot, Healthy).
func (in Installer) Install(ctx context.Context, from string, m Manifest, newBin string, step func(Step)) error {
	if step == nil {
		step = func(Step) {}
	}
	step(StepPreflight)
	if err := in.preflight(ctx, newBin); err != nil {
		return fmt.Errorf("the new version could not open a copy of the data: %w", err)
	}

	step(StepInstalling)
	schema := 0
	backup := filepath.Join(in.DataDir, "backups", fmt.Sprintf("update-%s-to-%s-%d", from, m.Version, in.now().Unix()))
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return err
	}
	if in.Snapshot != nil {
		var err error
		if schema, err = schemaOf(filepath.Join(in.DataDir, "metadata.db")); err != nil {
			return err
		}
		if err := in.Snapshot(ctx, filepath.Join(backup, "metadata.db")); err != nil {
			return fmt.Errorf("back up the database: %w", err)
		}
	}
	if in.WiredDir != "" {
		if err := copyDir(in.WiredDir, filepath.Join(backup, "wired")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("back up the wired state: %w", err)
		}
	}
	if err := copyFile(in.Exe, filepath.Join(backup, "nubilo")); err != nil {
		return fmt.Errorf("back up the binary: %w", err)
	}
	pruneUpdateBackups(filepath.Join(in.DataDir, "backups"), keepBackups)

	// The previous binary stays next to the new one: the service runs it
	// as a guard before each start, and it is what a rollback restores.
	if err := copyFile(in.Exe, in.Exe+".prev"); err != nil {
		return fmt.Errorf("keep the previous binary: %w", err)
	}
	st := &State{
		Phase: PhaseRestarting, From: from, To: m.Version, Exe: in.Exe, Backup: backup,
		WiredDir: in.WiredDir, StartedAt: in.now().UnixMilli(), SchemaBefore: schema,
	}
	if err := SaveState(in.DataDir, st); err != nil {
		return err
	}
	if err := copyFile(newBin, in.Exe); err != nil {
		st.Phase, st.Message, st.FinishedAt = PhaseFailed, "could not install the new binary: "+err.Error(), in.now().UnixMilli()
		_ = SaveState(in.DataDir, st)
		return fmt.Errorf("install the binary: %w", err)
	}
	return nil
}

// preflight runs the new binary's migrations on a snapshot of the data,
// or, without a database, just runs it: a binary built for a newer system
// (glibc) fails here, not after the swap.
func (in Installer) preflight(ctx context.Context, newBin string) error {
	if in.Snapshot == nil {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return in.run(ctx, newBin, "version")
	}
	if err := os.MkdirAll(stateDir(in.DataDir), 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(stateDir(in.DataDir), "preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	entries, err := os.ReadDir(in.DataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, "metadata.db") || strings.HasSuffix(name, ".log") {
			continue
		}
		if err := copyFile(filepath.Join(in.DataDir, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	for _, sub := range []string{"blobs", "tmp", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	if err := in.Snapshot(ctx, filepath.Join(dir, "metadata.db")); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return in.run(ctx, newBin, "update", "preflight", "--data-dir", dir)
}

// Boot is called by `nubilo server` before it opens the data. It finishes a
// rollback the previous run asked for, and counts the starts of a freshly
// installed binary, rolling back after too many. running is this binary's
// version. True means the binary was swapped back: exit and let the service
// manager start the restored one.
func Boot(dataDir, running string, now time.Time) (restart bool, err error) {
	st, err := LoadState(dataDir)
	if err != nil || st == nil {
		return false, err
	}
	switch st.Phase {
	case PhaseRollback:
		return false, restoreData(dataDir, st, now)
	case PhaseRestarting, PhaseVerifying:
		if running != st.To {
			// The old binary is running again (restored by hand, or the
			// swap failed): the update did not take.
			st.Phase, st.Message, st.FinishedAt = PhaseRolledBack, firstNonEmpty(st.Message, "the previous version is running again"), now.UnixMilli()
			return false, SaveState(dataDir, st)
		}
		st.Attempts++
		if st.Attempts > maxAttempts {
			return true, Rollback(dataDir, fmt.Sprintf("%s did not start (%d attempts)", st.To, st.Attempts-1), now)
		}
		st.Phase = PhaseVerifying
		return false, SaveState(dataDir, st)
	}
	return false, nil
}

// Guard runs from the previous binary before each start of the service
// (systemd ExecStartPre). It catches a new binary that crashes before it can
// count its own starts: past the deadline, it rolls back and restores the
// data so the restored binary starts on it.
func Guard(dataDir string, now time.Time) error {
	st, err := LoadState(dataDir)
	if err != nil || st == nil {
		return err
	}
	switch st.Phase {
	case PhaseRollback:
		return restoreData(dataDir, st, now)
	case PhaseRestarting, PhaseVerifying:
		if st.Attempts > maxAttempts || now.Sub(time.UnixMilli(st.StartedAt)) > startDeadline {
			if err := Rollback(dataDir, fmt.Sprintf("%s did not come up within %s", st.To, startDeadline), now); err != nil {
				return err
			}
			return restoreData(dataDir, mustLoad(dataDir), now)
		}
	}
	return nil
}

// Healthy marks the update done: the new binary is serving.
func Healthy(dataDir, running string, now time.Time) error {
	st, err := LoadState(dataDir)
	if err != nil || st == nil || st.Phase != PhaseVerifying || st.To != running {
		return err
	}
	st.Phase, st.FinishedAt, st.Message = PhaseDone, now.UnixMilli(), ""
	return SaveState(dataDir, st)
}

// Rollback puts the previous binary back and asks the next start to restore
// the database and wired state. It is safe while the new binary runs (the
// data is restored before anything opens it); the caller then exits so the
// service manager restarts.
func Rollback(dataDir, why string, now time.Time) error {
	st, err := LoadState(dataDir)
	if err != nil {
		return err
	}
	if st == nil {
		return errors.New("update: no update to roll back")
	}
	prev := st.Exe + ".prev"
	if _, err := os.Stat(prev); err != nil {
		prev = filepath.Join(st.Backup, "nubilo")
	}
	if err := copyFile(prev, st.Exe); err != nil {
		return fmt.Errorf("update: restore the previous binary: %w", err)
	}
	st.Phase, st.Message = PhaseRollback, why
	return SaveState(dataDir, st)
}

// restoreData brings back the database (only if the new version changed its
// schema; otherwise newer data is kept) and the wired state, before the
// restored binary opens them.
func restoreData(dataDir string, st *State, now time.Time) error {
	db := filepath.Join(dataDir, "metadata.db")
	if schema, err := schemaOf(db); err == nil && schema != st.SchemaBefore {
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(db + suffix)
		}
		if err := copyFile(filepath.Join(st.Backup, "metadata.db"), db); err != nil {
			return fmt.Errorf("update: restore the database: %w", err)
		}
	}
	if st.WiredDir != "" {
		if saved := filepath.Join(st.Backup, "wired"); dirExists(saved) {
			failed := fmt.Sprintf("%s.failed-%d", st.WiredDir, now.Unix())
			if err := os.Rename(st.WiredDir, failed); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("update: set aside the wired state: %w", err)
			}
			if err := copyDir(saved, st.WiredDir); err != nil {
				return fmt.Errorf("update: restore the wired state: %w", err)
			}
		}
	}
	st.Phase, st.FinishedAt = PhaseRolledBack, now.UnixMilli()
	return SaveState(dataDir, st)
}

// schemaOf reads the schema version of a database file without the store.
func schemaOf(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_meta`).Scan(&v); err != nil {
		return 0, fmt.Errorf("update: schema version of %s: %w", path, err)
	}
	return int(v.Int64), nil
}

func mustLoad(dataDir string) *State {
	st, _ := LoadState(dataDir)
	if st == nil {
		return &State{}
	}
	return st
}

func pruneUpdateBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "update-") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	// Oldest first by modification time.
	sort.Slice(names, func(i, j int) bool {
		a, _ := os.Stat(filepath.Join(dir, names[i]))
		b, _ := os.Stat(filepath.Join(dir, names[j]))
		return a != nil && b != nil && a.ModTime().Before(b.ModTime())
	})
	for _, n := range names[:len(names)-keep] {
		_ = os.RemoveAll(filepath.Join(dir, n))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// copyFile copies src over dst atomically (temp file in dst's directory,
// then rename), keeping src's mode. Replacing a running executable this way
// is safe: the running process keeps the old inode.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	ok = true
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			return copyFile(p, target)
		default:
			// Sockets (the anchor's admin.sock) are recreated on start.
			return nil
		}
	})
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
