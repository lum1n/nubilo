package cli

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"nubilo/internal/logging"
	"nubilo/internal/qr"
	"nubilo/internal/relay"
	"nubilo/internal/service"
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
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := relay.Run(ctx, dir, log); err != nil {
			return fatal(err)
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
