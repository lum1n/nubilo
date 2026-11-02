package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.8.0", "0.7.0", 1},
		{"0.7.0", "0.7.0-phase8", 1},
		{"v0.7.1", "0.7.0", 1},
		{"0.10.0", "0.9.9", 1},
		{"0.8.0-rc2", "0.8.0-rc1", 1},
		{"0.8.0", "0.8.0", 0},
		{"garbage", "0.1.0", -1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func manifestFor(t *testing.T, version, url string, bin []byte) Manifest {
	t.Helper()
	sum := sha256.Sum256(bin)
	return Manifest{
		Version: version, PublishedAt: 1, Notes: "notes", WiredProtocol: "3",
		Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: url, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}},
	}
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	m := manifestFor(t, "0.8.0", "https://example.com/nubilo", []byte("bin"))
	body, _ := json.Marshal(m)

	got, err := Verify(body, []byte(Sign(body, priv)+"\n"), []ed25519.PublicKey{otherPub, pub})
	if err != nil || got.Version != "0.8.0" {
		t.Fatalf("good signature: %v %v", got, err)
	}
	if _, err := Verify(body, []byte(Sign(body, otherPriv)), []ed25519.PublicKey{pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("other key: %v", err)
	}
	tampered := []byte(strings.Replace(string(body), "0.8.0", "0.9.0", 1))
	if _, err := Verify(tampered, []byte(Sign(body, priv)), []ed25519.PublicKey{pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := Verify(body, []byte(Sign(body, priv)), nil); !errors.Is(err, ErrSignature) {
		t.Fatalf("no keys: %v", err)
	}
	m.Artifacts[0].URL = "http://example.com/nubilo"
	plain, _ := json.Marshal(m)
	if _, err := Verify(plain, []byte(Sign(plain, priv)), []ed25519.PublicKey{pub}); !errors.Is(err, ErrManifest) {
		t.Fatalf("http artifact: %v", err)
	}
}

// releaseServer publishes one signed release of bin.
func releaseServer(t *testing.T, version string, bin []byte) (Source, Manifest) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	ts := httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	m := manifestFor(t, version, ts.URL+"/nubilo", bin)
	body, _ := json.Marshal(m)
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(Sign(body, priv))) })
	mux.HandleFunc("/nubilo", func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	return Source{ManifestURL: ts.URL + "/manifest.json", Keys: []ed25519.PublicKey{pub}, HTTP: ts.Client()}, m
}

func TestDownload(t *testing.T) {
	bin := []byte("#!/bin/sh\necho new\n")
	src, m := releaseServer(t, "0.8.0", bin)
	got, err := src.Latest(context.Background())
	if err != nil || got.Version != "0.8.0" {
		t.Fatalf("latest: %v %v", got, err)
	}
	a, _ := m.Artifact(runtime.GOOS, runtime.GOARCH)
	var last float64
	path, err := src.Download(context.Background(), a, t.TempDir(), func(p float64) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if string(b) != string(bin) || fi.Mode().Perm()&0o100 == 0 || last != 1 {
		t.Fatalf("download %q mode %v progress %v", b, fi.Mode(), last)
	}
	a.SHA256 = strings.Repeat("0", 64)
	if _, err := src.Download(context.Background(), a, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("bad hash: %v", err)
	}
}

// install sets up a data dir with a database at schema 1, wired state and
// a running binary, as a server would have.
type install struct {
	data, exe, wired string
	in               Installer
	preflightArgs    []string
}

func newInstall(t *testing.T) *install {
	t.Helper()
	root := t.TempDir()
	x := &install{data: filepath.Join(root, "data"), exe: filepath.Join(root, "bin", "nubilo"), wired: filepath.Join(root, "data", "wired")}
	for _, d := range []string{x.data, filepath.Dir(x.exe), x.wired} {
		_ = os.MkdirAll(d, 0o700)
	}
	_ = os.WriteFile(x.exe, []byte("old binary"), 0o755)
	_ = os.WriteFile(filepath.Join(x.wired, "home.state"), []byte("wired-state/2\n"), 0o600)
	_ = os.WriteFile(filepath.Join(x.data, "config.json"), []byte("{}"), 0o600)
	setSchema(t, filepath.Join(x.data, "metadata.db"), 1)
	x.in = Installer{
		DataDir: x.data, Exe: x.exe, WiredDir: x.wired,
		Snapshot: func(ctx context.Context, path string) error {
			db, err := sql.Open("sqlite", filepath.Join(x.data, "metadata.db"))
			if err != nil {
				return err
			}
			defer db.Close()
			_, err = db.ExecContext(ctx, `VACUUM INTO ?`, path)
			return err
		},
		Run: func(ctx context.Context, bin string, args ...string) error {
			x.preflightArgs = append([]string{bin}, args...)
			// The new version migrates its copy.
			setSchema(t, filepath.Join(args[len(args)-1], "metadata.db"), 2)
			if _, err := os.Stat(filepath.Join(args[len(args)-1], "config.json")); err != nil {
				return errors.New("no config in the preflight copy")
			}
			return nil
		},
	}
	return x
}

func setSchema(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta(version INTEGER); INSERT INTO schema_meta VALUES (?)`, v); err != nil {
		t.Fatal(err)
	}
}

func (x *install) newBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "download")
	_ = os.WriteFile(p, []byte("new binary"), 0o755)
	return p
}

func read(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestInstallThenHealthy(t *testing.T) {
	x := newInstall(t)
	now := time.Now()
	var steps []Step
	if err := x.in.Install(context.Background(), "0.7.0", Manifest{Version: "0.8.0"}, x.newBinary(t), func(s Step) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	if read(x.exe) != "new binary" || read(x.exe+".prev") != "old binary" {
		t.Fatalf("swap: exe=%q prev=%q", read(x.exe), read(x.exe+".prev"))
	}
	if len(steps) != 2 || x.preflightArgs[1] != "update" || x.preflightArgs[2] != "preflight" {
		t.Fatalf("steps %v preflight %v", steps, x.preflightArgs)
	}
	if s, _ := schemaOf(filepath.Join(x.data, "metadata.db")); s != 1 {
		t.Fatalf("preflight touched the live database: schema %d", s)
	}
	st, _ := LoadState(x.data)
	if st.Phase != PhaseRestarting || st.SchemaBefore != 1 || read(filepath.Join(st.Backup, "wired", "home.state")) != "wired-state/2\n" {
		t.Fatalf("state %+v", st)
	}

	// The new binary starts, migrates, and reports healthy.
	if restart, err := Boot(x.data, "0.8.0", now); restart || err != nil {
		t.Fatalf("boot: %v %v", restart, err)
	}
	setSchema(t, filepath.Join(x.data, "metadata.db"), 2)
	if err := Healthy(x.data, "0.8.0", now); err != nil {
		t.Fatal(err)
	}
	if st, _ := LoadState(x.data); st.Phase != PhaseDone || st.Attempts != 1 {
		t.Fatalf("after healthy %+v", st)
	}
	// Later starts and guards leave a finished update alone.
	if restart, _ := Boot(x.data, "0.8.0", now); restart {
		t.Fatal("restart after done")
	}
	if err := Guard(x.data, now.Add(time.Hour)); err != nil || read(x.exe) != "new binary" {
		t.Fatalf("guard after done: %v %q", err, read(x.exe))
	}
}

func TestPreflightFailureChangesNothing(t *testing.T) {
	x := newInstall(t)
	x.in.Run = func(ctx context.Context, bin string, args ...string) error { return errors.New("migration v9 failed") }
	err := x.in.Install(context.Background(), "0.7.0", Manifest{Version: "0.8.0"}, x.newBinary(t), nil)
	if err == nil || !strings.Contains(err.Error(), "migration v9 failed") {
		t.Fatalf("install: %v", err)
	}
	if read(x.exe) != "old binary" {
		t.Fatal("binary replaced after a failed preflight")
	}
	if _, err := os.Stat(x.exe + ".prev"); err == nil {
		t.Fatal("prev written after a failed preflight")
	}
}

func TestUnhealthyRollsBack(t *testing.T) {
	x := newInstall(t)
	now := time.Now()
	if err := x.in.Install(context.Background(), "0.7.0", Manifest{Version: "0.8.0"}, x.newBinary(t), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Boot(x.data, "0.8.0", now); err != nil {
		t.Fatal(err)
	}
	// The new version migrated and wrote new wired state, then never got
	// healthy (could not join the relay).
	setSchema(t, filepath.Join(x.data, "metadata.db"), 2)
	_ = os.WriteFile(filepath.Join(x.wired, "home.state"), []byte("wired-state/3\n"), 0o600)
	if err := Rollback(x.data, "0.8.0 did not become healthy", now); err != nil {
		t.Fatal(err)
	}
	if read(x.exe) != "old binary" {
		t.Fatal("binary not restored")
	}
	// The restored binary's guard (or its own Boot) restores the data
	// before opening it.
	if err := Guard(x.data, now); err != nil {
		t.Fatal(err)
	}
	if s, _ := schemaOf(filepath.Join(x.data, "metadata.db")); s != 1 {
		t.Fatalf("database schema %d after rollback", s)
	}
	if read(filepath.Join(x.wired, "home.state")) != "wired-state/2\n" {
		t.Fatal("wired state not restored")
	}
	st, _ := LoadState(x.data)
	if st.Phase != PhaseRolledBack || !strings.Contains(st.Message, "healthy") {
		t.Fatalf("state %+v", st)
	}
}

func TestRollbackKeepsDataWhenSchemaUnchanged(t *testing.T) {
	x := newInstall(t)
	now := time.Now()
	_ = x.in.Install(context.Background(), "0.7.0", Manifest{Version: "0.8.0"}, x.newBinary(t), nil)
	_, _ = Boot(x.data, "0.8.0", now)
	// Writes made by the new version at the same schema survive.
	db, _ := sql.Open("sqlite", filepath.Join(x.data, "metadata.db"))
	_, _ = db.Exec(`CREATE TABLE notes(x TEXT); INSERT INTO notes VALUES ('kept')`)
	db.Close()
	_ = Rollback(x.data, "test", now)
	_ = Guard(x.data, now)
	db, _ = sql.Open("sqlite", filepath.Join(x.data, "metadata.db"))
	defer db.Close()
	var got string
	if err := db.QueryRow(`SELECT x FROM notes`).Scan(&got); err != nil || got != "kept" {
		t.Fatalf("data lost: %q %v", got, err)
	}
}

func TestCrashLoopRollsBack(t *testing.T) {
	x := newInstall(t)
	now := time.Now()
	_ = x.in.Install(context.Background(), "0.7.0", Manifest{Version: "0.8.0"}, x.newBinary(t), nil)
	// The new binary keeps dying after counting its start.
	for i := 0; i < maxAttempts; i++ {
		if restart, err := Boot(x.data, "0.8.0", now); restart || err != nil {
			t.Fatalf("start %d: %v %v", i+1, restart, err)
		}
	}
	restart, err := Boot(x.data, "0.8.0", now)
	if !restart || err != nil || read(x.exe) != "old binary" {
		t.Fatalf("4th start: restart=%v err=%v exe=%q", restart, err, read(x.exe))
	}
}

func TestGuardCatchesBinaryThatNeverStarts(t *testing.T) {
	x := newInstall(t)
	now := time.Now()
	_ = x.in.Install(context.Background(), "0.7.0", Manifest{Version: "0.8.0"}, x.newBinary(t), nil)
	// The new binary crashes before Boot: only the guard runs.
	if err := Guard(x.data, now.Add(time.Minute)); err != nil || read(x.exe) != "new binary" {
		t.Fatalf("within deadline: %v %q", err, read(x.exe))
	}
	if err := Guard(x.data, now.Add(startDeadline+time.Second)); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(x.data)
	if read(x.exe) != "old binary" || st.Phase != PhaseRolledBack {
		t.Fatalf("after deadline: exe=%q state=%+v", read(x.exe), st)
	}
}

func TestManager(t *testing.T) {
	bin := []byte("new binary")
	src, _ := releaseServer(t, "0.8.0", bin)
	x := newInstall(t)
	restarted := make(chan struct{}, 1)
	clock := time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	m := &Manager{
		DataDir: x.data, Current: "0.7.0", Source: src, Install: x.in, RelayProtocol: "3",
		Restart: func() { restarted <- struct{}{} }, Now: func() time.Time { return clock },
	}
	st := m.Status()
	if st.Available != nil || !st.Auto || st.State != "idle" {
		t.Fatalf("before check %+v", st)
	}
	st, err := m.Check(context.Background())
	if err != nil || st.Available == nil || st.Available.Version != "0.8.0" || st.Blocked != "" || st.CheckedAt == 0 {
		t.Fatalf("check %+v %v", st, err)
	}
	// The window is 03:00–05:00 the next night.
	if next := time.UnixMilli(st.NextAutoAt); next.Day() != 3 || next.Hour() < 3 || next.Hour() > 4 {
		t.Fatalf("next window %v", next)
	}
	// Midday: no automatic install.
	m.maybeAutoInstall()
	select {
	case <-restarted:
		t.Fatal("installed outside the window")
	default:
	}

	// A relay on another protocol blocks it.
	m.RelayProtocol = "2"
	if st := m.Status(); !strings.Contains(st.Blocked, "relay protocol (2 to 3)") {
		t.Fatalf("blocked %q", st.Blocked)
	}
	if _, err := m.StartInstall(); err == nil {
		t.Fatal("installed a blocked release")
	}
	m.RelayProtocol = "3"

	// In the window it installs and restarts.
	clock = time.UnixMilli(st.NextAutoAt).Add(10 * time.Minute)
	m.maybeAutoInstall()
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("no restart; status %+v", m.Status())
	}
	if read(x.exe) != "new binary" || m.Status().State != "restarting" {
		t.Fatalf("exe %q state %q", read(x.exe), m.Status().State)
	}

	// Turned off: no window.
	if st, err := m.SetAuto(false); err != nil || st.Auto || st.NextAutoAt != 0 {
		t.Fatalf("auto off %+v %v", st, err)
	}
}

func TestManagerSkipsReleaseThatRolledBack(t *testing.T) {
	src, _ := releaseServer(t, "0.8.0", []byte("new binary"))
	x := newInstall(t)
	_ = SaveState(x.data, &State{Phase: PhaseRolledBack, From: "0.7.0", To: "0.8.0", Message: "did not start", FinishedAt: 1})
	restarted := make(chan struct{}, 1)
	m := &Manager{DataDir: x.data, Current: "0.7.0", Source: src, Install: x.in, Restart: func() { restarted <- struct{}{} }}
	st, _ := m.Check(context.Background())
	if st.Last == nil || st.Last.Result != "rolled_back" {
		t.Fatalf("last %+v", st.Last)
	}
	m.Now = func() time.Time { return time.UnixMilli(st.NextAutoAt).Add(time.Minute) }
	m.maybeAutoInstall()
	select {
	case <-restarted:
		t.Fatal("reinstalled a release that rolled back")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBlockedInsideMacApp(t *testing.T) {
	src, _ := releaseServer(t, "0.8.0", []byte("x"))
	m := &Manager{DataDir: t.TempDir(), Current: "0.7.0", Source: src, Install: Installer{Exe: "/Applications/Nubilo.app/Contents/Helpers/nubilo"}}
	st, _ := m.Check(context.Background())
	if !strings.Contains(st.Blocked, "Update the app") {
		t.Fatalf("blocked %q", st.Blocked)
	}
}
