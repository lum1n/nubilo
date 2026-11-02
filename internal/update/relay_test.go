package update

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay plays a relay's updater: Install starts its update, and the
// next status calls report how it went.
type fakeRelay struct {
	mu        sync.Mutex
	current   string
	protocol  string
	last      *Outcome
	err       error // Status error (unreachable)
	installed []string
	// after: what the relay does once asked to install.
	after func(f *fakeRelay, version string)
}

func (f *fakeRelay) Status(ctx context.Context) (Status, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Status{}, "", f.err
	}
	return Status{Current: f.current, State: "idle", Last: f.last}, f.protocol, nil
}

func (f *fakeRelay) Install(ctx context.Context, version string) error {
	f.mu.Lock()
	f.installed = append(f.installed, version)
	after := f.after
	f.mu.Unlock()
	if after != nil {
		go func() {
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
			defer f.mu.Unlock()
			after(f, version)
		}()
	}
	return nil
}

func updatedTo(f *fakeRelay, v string) {
	f.last = &Outcome{From: f.current, To: v, Result: "updated", At: time.Now().UnixMilli()}
	f.current = v
}

// homeWithRelay: a home server speaking homeProtocol; the release it
// finds speaks protocol 3.
func homeWithRelay(t *testing.T, relay *fakeRelay, homeProtocol string) (*Manager, *install, chan struct{}) {
	t.Helper()
	relayPoll = 10 * time.Millisecond
	t.Cleanup(func() { relayPoll = 3 * time.Second })
	src, _ := releaseServer(t, "0.8.0", []byte("new binary"))
	x := newInstall(t)
	restarted := make(chan struct{}, 1)
	m := &Manager{
		DataDir: x.data, Current: "0.7.0", Source: src, Install: x.in, RelayProtocol: homeProtocol,
		Relay: relay, RelayHealthyWithin: 2 * time.Second, Restart: func() { restarted <- struct{}{} },
	}
	return m, x, restarted
}

func waitRestart(t *testing.T, m *Manager, restarted chan struct{}) {
	t.Helper()
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("no restart; status %+v", m.Status())
	}
}

func TestRelayUpdatesFirst(t *testing.T) {
	relay := &fakeRelay{current: "0.7.0", protocol: "3", after: updatedTo}
	m, x, restarted := homeWithRelay(t, relay, "3")
	st, err := m.Check(context.Background())
	if err != nil || st.Relay == nil || !st.Relay.Reachable || st.Relay.Current != "0.7.0" || st.Blocked != "" {
		t.Fatalf("check %+v relay %+v %v", st, st.Relay, err)
	}
	if _, err := m.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitRestart(t, m, restarted)
	if len(relay.installed) != 1 || relay.installed[0] != "0.8.0" || read(x.exe) != "new binary" {
		t.Fatalf("relay installs %v, home exe %q", relay.installed, read(x.exe))
	}
}

func TestRelayFailureStopsHome(t *testing.T) {
	relay := &fakeRelay{current: "0.7.0", protocol: "3", after: func(f *fakeRelay, v string) {
		f.last = &Outcome{From: "0.7.0", To: v, Result: "rolled_back", Message: "the home server did not reconnect"}
	}}
	m, x, _ := homeWithRelay(t, relay, "3")
	_, _ = m.Check(context.Background())
	if _, err := m.StartInstall(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.Status(); st.State == "idle" && st.Last != nil {
			if st.Last.Result != "failed" || !strings.Contains(st.Last.Message, "relay could not update") {
				t.Fatalf("last %+v", st.Last)
			}
			if read(x.exe) != "old binary" {
				t.Fatal("home updated although its relay failed")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("still busy: %+v", m.Status())
}

func TestRelayAlreadyCurrentOrUnreachable(t *testing.T) {
	for name, relay := range map[string]*fakeRelay{
		"current":     {current: "0.8.0", protocol: "3"},
		"unreachable": {err: errors.New("the relay did not answer")},
	} {
		t.Run(name, func(t *testing.T) {
			m, x, restarted := homeWithRelay(t, relay, "3")
			_, _ = m.Check(context.Background())
			if _, err := m.StartInstall(); err != nil {
				t.Fatal(err)
			}
			waitRestart(t, m, restarted)
			if len(relay.installed) != 0 || read(x.exe) != "new binary" {
				t.Fatalf("relay installs %v, home exe %q", relay.installed, read(x.exe))
			}
		})
	}
}

func TestProtocolChangeNeedsAnUpdatableRelay(t *testing.T) {
	// Home and relay speak protocol 2; the release speaks 3.
	unreachable := &fakeRelay{err: errors.New("the relay did not answer (it may be too old to update itself)")}
	m, _, _ := homeWithRelay(t, unreachable, "2")
	st, _ := m.Check(context.Background())
	if !strings.Contains(st.Blocked, "relay has to update first") || !strings.Contains(st.Blocked, "too old") {
		t.Fatalf("blocked %q", st.Blocked)
	}

	// An updatable relay goes first; once it has restarted (unreachable on
	// the new protocol), home follows.
	relay := &fakeRelay{current: "0.7.0", protocol: "2", after: func(f *fakeRelay, v string) {
		f.err = errors.New("connection refused")
	}}
	m, x, restarted := homeWithRelay(t, relay, "2")
	if st, _ := m.Check(context.Background()); st.Blocked != "" {
		t.Fatalf("blocked %q", st.Blocked)
	}
	if _, err := m.StartInstall(); err != nil {
		t.Fatal(err)
	}
	waitRestart(t, m, restarted)
	if len(relay.installed) != 1 || read(x.exe) != "new binary" {
		t.Fatalf("relay installs %v, home exe %q", relay.installed, read(x.exe))
	}
}
