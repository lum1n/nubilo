package relay

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"nubilo/internal/wiredx"
)

// fakeNode "dials" overlay targets by looking them up in a map of TCP addresses.
type fakeNode struct {
	targets map[string]string
	revoked []string
}

func (f *fakeNode) Name() string                        { return "relay" }
func (f *fakeNode) Listen(string) (net.Listener, error) { return nil, wiredx.ErrUnavailable }
func (f *fakeNode) Invite() string                      { return "wired-invite-1" }
func (f *fakeNode) Members() []wiredx.Member            { return []wiredx.Member{{Name: "home", ID: "k"}} }
func (f *fakeNode) RotateInvite() (string, error)       { return "wired-invite-2", nil }
func (f *fakeNode) Path(string) string                  { return "direct" }
func (f *fakeNode) Close() error                        { return nil }
func (f *fakeNode) Revoke(n string) (string, error) {
	f.revoked = append(f.revoked, n)
	return "wired-invite-3", nil
}
func (f *fakeNode) Dial(ctx context.Context, target string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", f.targets[target])
}

func TestRelayForwardsAndControl(t *testing.T) {
	home, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	go func() {
		for {
			c, err := home.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("hello from home"))
			c.Close()
		}
	}()
	fake := &fakeNode{targets: map[string]string{"https@home": home.Addr().String()}}
	anchor = func(wiredx.AnchorOptions) (wiredx.Node, error) { return fake, nil }
	defer func() { anchor = wiredx.Anchor }()

	// Pick a free port for the public side.
	pl, _ := net.Listen("tcp", "127.0.0.1:0")
	pub := pl.Addr().String()
	pl.Close()
	dir := t.TempDir()
	if err := Save(dir, Config{Name: "relay", PublicHost: "203.0.113.7", Forward: []Forward{{Listen: pub, Target: "https@home"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dir, nil) }()
	var c net.Conn
	for i := 0; i < 100; i++ {
		if c, err = net.Dial("tcp", pub); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c == nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	c.Close()
	if string(b) != "hello from home" {
		t.Fatalf("forwarded %q", b)
	}
	var members []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := Control(dir, "GET", "/members", &members); err != nil || len(members) != 1 || members[0].Path != "direct" {
		t.Fatalf("members %v %v", members, err)
	}
	var inv map[string]string
	if err := Control(dir, "POST", "/revoke/laptop", &inv); err != nil || inv["invite"] != "wired-invite-3" || fake.revoked[0] != "laptop" {
		t.Fatalf("revoke %v %v", inv, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop")
	}
	if err := Control(dir, "GET", "/members", &members); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("control after stop: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	if err := Save(t.TempDir(), Config{}); err == nil {
		t.Fatal("empty public host accepted")
	}
	c := DefaultConfig("relay.example.com", "")
	if c.Forward[0].Target != "https@home" || c.Forward[1].Listen != ":80" {
		t.Fatalf("defaults %+v", c)
	}
}
