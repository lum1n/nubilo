package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"nubilo/internal/logging"
	"nubilo/internal/qr"
	"nubilo/internal/relay"
	"nubilo/internal/service"
	"nubilo/internal/update"
	"nubilo/internal/version"
	"nubilo/internal/wiredx"
)

// relayDir is the relay's data dir: --data-dir, else ~/.nubilo-relay.
func relayDir(g global) (string, error) {
	if g.dataDir != "" {
		return filepath.Abs(g.dataDir)
	}
	if v := os.Getenv("NUBILO_RELAY_DIR"); v != "" {
		return filepath.Abs(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nubilo-relay"), nil
}

func runRelay(g global, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nubilo relay init|run|install|uninstall|invite|members|revoke")
		return 2
	}
	sub, args := args[0], args[1:]
	dir, err := relayDir(g)
	if err != nil {
		return fatal(err)
	}
	switch sub {
	case "init":
		fs := flag.NewFlagSet("relay init", flag.ContinueOnError)
		host := fs.String("public-host", "", "public IP or DNS name of this VPS (put into the invite)")
		home := fs.String("home", "home", "overlay node name of the home server")
		proxyURL := fs.String("behind-proxy", "", "https URL of a TLS-terminating proxy in front of this host (exe.dev: https://<vm>.exe.xyz)")
		listen := fs.String("listen", relay.EdgeListen, "with --behind-proxy: the plain-HTTP address the proxy forwards to")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if *host == "" && *proxyURL == "" {
			return fatal(fmt.Errorf("relay init requires --public-host (a VPS) or --behind-proxy URL (exe.dev)"))
		}
		if !wiredx.Available() {
			return fatal(wiredx.ErrUnavailable)
		}
		cfg := relay.DefaultConfig(*host, *home)
		if *proxyURL != "" {
			if cfg, err = relay.EdgeDefaultConfig(*proxyURL, *listen, *home); err != nil {
				return fatal(err)
			}
		}
		inv, err := relay.Init(dir, cfg)
		if err != nil {
			return fatal(err)
		}
		fmt.Printf("relay initialized in %s\n\n", dir)
		printRelayInvite(inv)
		if e := cfg.Edge; e != nil {
			_, port, _ := net.SplitHostPort(e.Listen)
			fmt.Printf("\nBehind a proxy: point it at port %s (exe.dev: ssh exe.dev share port <vm> %s; ssh exe.dev share set-public <vm>).\n", port, port)
			fmt.Printf("The proxy terminates TLS for %s and can read the traffic; wired encrypts the hop to your home.\n", e.URL)
			fmt.Println("Then: nubilo relay install   (or run it in the foreground: nubilo relay run)")
			fmt.Printf("At home: nubilo setup --mode proxy --domain %s --relay-invite '<invite above>'\n", cfg.PublicHost)
			return 0
		}
		fmt.Printf("\nOpen on this VPS: TCP 80, 443, %d and UDP %d, %d.\n", wiredx.AnchorPort, wiredx.AnchorPort, wiredx.QADPort)
		fmt.Println("Then: nubilo relay install   (or run it in the foreground: nubilo relay run)")
		fmt.Println("At home: nubilo setup, choose \"relay\", and paste the invite above.")
		return 0
	case "run":
		log := logging.New(logging.Options{Level: "info"})
		if restart, err := update.Boot(dir, version.String, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "update:", err)
		} else if restart {
			fmt.Fprintln(os.Stderr, "update: rolled back to the previous version; restarting")
			return exitRestart
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var restarting atomic.Bool
		restart := func() { restarting.Store(true); cancel() }
		ready := func(ctx context.Context, node wiredx.Node) {
			m := newRelayUpdater(dir, log, restart)
			if m == nil {
				return
			}
			if err := relay.ServeAdmin(ctx, node, m, log); err != nil {
				log.Error("relay_admin", "err", err.Error())
			}
			go watchRelayUpdate(ctx, node, dir, log, restart)
		}
		if err := relay.RunWith(ctx, dir, log, ready); err != nil && !restarting.Load() {
			return fatal(err)
		}
		if restarting.Load() {
			return exitRestart
		}
		return 0
	case "install":
		exe, err := service.ResolveExe()
		if err != nil {
			return fatal(err)
		}
		if _, err := relay.Load(dir); err != nil {
			return fatal(err)
		}
		if err := service.EnsureLogDir(dir); err != nil {
			return fatal(err)
		}
		path, err := service.Install(service.Spec{Kind: service.KindRelay, Exe: exe, DataDir: dir})
		if err != nil {
			return fatal(err)
		}
		fmt.Printf("relay service installed: %s\n", path)
		fmt.Fprintln(os.Stderr, "note: loginctl enable-linger $USER so it survives logout; low ports need sysctl net.ipv4.ip_unprivileged_port_start=80")
		return 0
	case "uninstall":
		if err := service.Uninstall(service.KindRelay); err != nil {
			return fatal(err)
		}
		fmt.Println("relay service removed")
		return 0
	case "invite":
		fs := flag.NewFlagSet("relay invite", flag.ContinueOnError)
		rotate := fs.Bool("rotate", false, "replace the invite (existing members stay joined)")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		var out map[string]string
		method, path := "GET", "/invite"
		if *rotate {
			method, path = "POST", "/invite/rotate"
		}
		if err := relay.Control(dir, method, path, &out); err != nil {
			return fatal(err)
		}
		printRelayInvite(out["invite"])
		return 0
	case "members":
		var out []map[string]any
		if err := relay.Control(dir, "GET", "/members", &out); err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(out)
		}
		for _, m := range out {
			role := "member"
			if a, _ := m["anchor"].(bool); a {
				role = "anchor"
			}
			fmt.Printf("%-16v %-7s path=%v\n", m["name"], role, m["path"])
		}
		return 0
	case "revoke":
		if len(args) != 1 {
			return fatal(fmt.Errorf("relay revoke NAME"))
		}
		var out map[string]string
		if err := relay.Control(dir, "POST", "/revoke/"+args[0], &out); err != nil {
			return fatal(err)
		}
		fmt.Printf("revoked %s; the invite was rotated:\n", args[0])
		printRelayInvite(out["invite"])
		return 0
	default:
		return fatal(fmt.Errorf("unknown relay subcommand %s", sub))
	}
}

func printRelayInvite(inv string) {
	if text, err := qr.Terminal(inv); err == nil {
		fmt.Print(text)
	}
	fmt.Printf("Relay invite (a secret; anyone holding it can join the overlay):\n%s\n", inv)
}

// newRelayUpdater sets up updates for this relay: installed when its home
// server asks (over the overlay), never on the relay's own schedule, so the
// home server can put them in order.
func newRelayUpdater(dir string, log *slog.Logger, restart func()) *update.Manager {
	exe, err := service.ResolveExe()
	if err != nil {
		log.Warn("update_disabled", "err", err.Error())
		return nil
	}
	if changed, err := service.Refresh(service.Spec{Kind: service.KindRelay, Exe: exe, DataDir: dir}); err != nil {
		log.Warn("service_refresh", "err", err.Error())
	} else if changed {
		log.Info("service_refresh", "unit", service.KindRelay.UnitName())
	}
	return &update.Manager{
		DataDir: dir,
		Current: version.String,
		Source:  update.DefaultSource(),
		Install: update.Installer{DataDir: dir, Exe: exe, WiredDir: filepath.Join(dir, "wired")},
		Manual:  true,
		Restart: restart,
		Log:     log,
	}
}

// relayHealthyWithin is how long a freshly updated relay has for its home
// server to reconnect before it rolls back. Longer than the server's own
// limit: across a wired protocol change, the home server updates only
// after the relay has restarted.
const relayHealthyWithin = 10 * time.Minute

// relayHealthyOverride shortens relayHealthyWithin in test builds of the
// update loop (-ldflags "-X nubilo/internal/cli.relayHealthyOverride=1m").
var relayHealthyOverride = ""

func relayDeadline() time.Duration {
	if d, err := time.ParseDuration(relayHealthyOverride); err == nil && d > 0 {
		return d
	}
	return relayHealthyWithin
}

// watchRelayUpdate finishes an update this relay binary was installed by:
// the home server must reconnect in time, or it goes back.
func watchRelayUpdate(ctx context.Context, node wiredx.Node, dir string, log *slog.Logger, restart func()) {
	st, err := update.LoadState(dir)
	if err != nil || st == nil || st.Phase != update.PhaseVerifying || st.To != version.String {
		return
	}
	deadline := time.Now().Add(relayDeadline())
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		if relay.Reconnected(node) {
			if err := update.Healthy(dir, version.String, time.Now()); err != nil {
				log.Error("update_healthy", "err", err.Error())
			} else {
				log.Info("update_done", "from", st.From, "to", st.To)
			}
			return
		}
	}
	why := fmt.Sprintf("the home server did not reconnect to relay %s within %s", st.To, relayDeadline())
	log.Error("update_rollback", "why", why)
	if err := update.Rollback(dir, why, time.Now()); err != nil {
		log.Error("update_rollback", "err", err.Error())
		return
	}
	restart()
}
