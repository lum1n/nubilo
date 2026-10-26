// Package relay runs on a small public VPS. It anchors a wired overlay that
// the home server joins, and forwards public TCP 443/80 into it as raw
// bytes: TLS terminates on the home server, so the VPS never holds the
// master key, the database, or a certificate private key, and sees only
// ciphertext. It can deny service; it cannot read data.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nubilo/internal/wiredx"
)

// Forward maps a public TCP listen address to an overlay target.
type Forward struct {
	Listen string `json:"listen"`
	Target string `json:"target"` // service@node
}

// Config is relay.json in the relay data dir.
type Config struct {
	Name       string    `json:"name"`
	PublicHost string    `json:"public_host"`
	Port       uint16    `json:"port,omitempty"`
	Forward    []Forward `json:"forward"`
}

// anchor starts the overlay anchor; tests replace it with a fake.
var anchor = wiredx.Anchor

const (
	ConfigFile  = "relay.json"
	ControlSock = "control.sock"
	stateSubdir = "wired"
)

// DefaultConfig forwards public 443/80 to the home server's overlay services.
func DefaultConfig(publicHost, homeNode string) Config {
	if homeNode == "" {
		homeNode = "home"
	}
	return Config{
		Name:       "relay",
		PublicHost: publicHost,
		Forward: []Forward{
			{Listen: ":443", Target: wiredx.ServiceHTTPS + "@" + homeNode},
			{Listen: ":80", Target: wiredx.ServiceHTTP + "@" + homeNode},
		},
	}
}

func Load(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return c, fmt.Errorf("relay: not initialized in %s (run `nubilo relay init --public-host HOST`)", dir)
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("relay: %s: %w", ConfigFile, err)
	}
	return c, c.validate()
}

func Save(dir string, c Config) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(dir, ConfigFile), append(b, '\n'), 0o600)
}

func (c Config) validate() error {
	if strings.TrimSpace(c.PublicHost) == "" {
		return errors.New("relay: public_host is required")
	}
	for _, f := range c.Forward {
		if _, _, err := net.SplitHostPort(f.Listen); err != nil {
			return fmt.Errorf("relay: forward listen %q: %w", f.Listen, err)
		}
		if !strings.Contains(f.Target, "@") {
			return fmt.Errorf("relay: forward target %q must be service@node", f.Target)
		}
	}
	return nil
}

func (c Config) anchorOptions(dir string) wiredx.AnchorOptions {
	return wiredx.AnchorOptions{Name: c.Name, PublicHost: c.PublicHost, Port: c.Port, StateDir: filepath.Join(dir, stateSubdir)}
}

// Init starts the anchor once to create its state and returns the invite.
func Init(dir string, c Config) (string, error) {
	if err := Save(dir, c); err != nil {
		return "", err
	}
	node, err := anchor(c.anchorOptions(dir))
	if err != nil {
		return "", err
	}
	defer node.Close()
	return node.Invite(), nil
}

// Run anchors the overlay, forwards the configured ports, and serves the
// local control socket until ctx ends.
func Run(ctx context.Context, dir string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	c, err := Load(dir)
	if err != nil {
		return err
	}
	node, err := anchor(c.anchorOptions(dir))
	if err != nil {
		return err
	}
	defer node.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(c.Forward)+1)
	var wg sync.WaitGroup
	for _, f := range c.Forward {
		ln, err := net.Listen("tcp", f.Listen)
		if err != nil {
			return fmt.Errorf("relay: listen %s: %w (on Linux, allow low ports: sysctl net.ipv4.ip_unprivileged_port_start=80)", f.Listen, err)
		}
		target := f.Target
		log.Info("relay_forward", "listen", f.Listen, "target", target)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- wiredx.Forward(ctx, ln, func(ctx context.Context) (net.Conn, error) { return node.Dial(ctx, target) }, log,
				wiredx.ForwardOptions{SendProxyHeader: true})
		}()
	}
	ctl, err := serveControl(ctx, dir, node, log)
	if err != nil {
		return err
	}
	defer ctl.Close()
	log.Info("relay_ready", "name", node.Name(), "public_host", c.PublicHost)
	var first error
	select {
	case <-ctx.Done():
	case first = <-errCh:
		cancel()
	}
	wg.Wait()
	return first
}

// Control requests are served on a 0600 unix socket in the relay dir, so
// `nubilo relay members|revoke|invite` talk to the running anchor instead of
// starting a second one on the same ports.
func serveControl(ctx context.Context, dir string, node wiredx.Node, log *slog.Logger) (net.Listener, error) {
	path := filepath.Join(dir, ControlSock)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /members", func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			wiredx.Member
			Path string `json:"path"`
		}
		var out []row
		for _, m := range node.Members() {
			out = append(out, row{Member: m, Path: node.Path(m.Name)})
		}
		reply(w, out, nil)
	})
	mux.HandleFunc("GET /invite", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"invite": node.Invite()}, nil)
	})
	mux.HandleFunc("POST /invite/rotate", func(w http.ResponseWriter, r *http.Request) {
		inv, err := node.RotateInvite()
		reply(w, map[string]string{"invite": inv}, err)
	})
	mux.HandleFunc("POST /revoke/{name}", func(w http.ResponseWriter, r *http.Request) {
		inv, err := node.Revoke(r.PathValue("name"))
		log.Info("relay_revoke", "name", r.PathValue("name"), "ok", err == nil)
		reply(w, map[string]string{"invite": inv}, err)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = os.Remove(path)
	}()
	return ln, nil
}

// Control calls the running relay's control socket.
func Control(dir, method, path string, out any) error {
	sock := filepath.Join(dir, ControlSock)
	c := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
	req, _ := http.NewRequest(method, "http://relay"+path, nil)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("relay: not running (is `nubilo relay run` up?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("relay: %s", e.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
