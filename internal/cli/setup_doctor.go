package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"nubilo/internal/agent"
	"nubilo/internal/app"
	"nubilo/internal/doctor"
	"nubilo/internal/setup"
)

func runDoctor(g global, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	verify := fs.Bool("verify", false, "run integrity verify (slower)")
	agentMode := fs.Bool("agent", false, "check agent data dir instead of server")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	var rep doctor.Report
	useAgent := *agentMode || (g.dataDir != "" && isAgentDataDir(dir))
	if useAgent {
		rep, err = doctor.Agent(dir)
	} else {
		rep, err = doctor.Server(context.Background(), dir, doctor.Options{Verify: *verify})
	}
	if err != nil {
		return fatal(err)
	}
	if *jsonOut || g.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Print(doctor.FormatHuman(rep))
	}
	if !rep.Healthy {
		return 1
	}
	return 0
}

func isServerDataDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "master.key"))
	return err == nil
}

func isAgentDataDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "device.json"))
	return err == nil && !isServerDataDir(dir)
}

func runSetup(g global, args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:8443", "LAN listen address for new installs")
	yes := fs.Bool("yes", false, "non-interactive: use flags and defaults")
	noService := fs.Bool("no-service", false, "skip always-on service install")
	noBackup := fs.Bool("no-backup", false, "skip enabling encrypted auto-backup")
	mode := fs.String("mode", "", "how devices reach the server: relay, direct or lan")
	domain := fs.String("domain", "", "public DNS name (relay and direct modes)")
	email := fs.String("email", "", "Let's Encrypt account email (optional)")
	invite := fs.String("relay-invite", "", "invite printed by `nubilo relay init` on the VPS")
	staging := fs.Bool("acme-staging", false, "use the Let's Encrypt staging CA (dry run)")
	jsonOut := fs.Bool("json", false, "print one JSON object per step (for the Mac app)")
	step := fs.Int("step", 0, "run only this step (1 init, 2 reachability, 3 service, 4 verify, 5 invite)")
	timeout := fs.Duration("verify-timeout", 2*time.Minute, "how long to wait for the server to answer")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	asJSON := *jsonOut || g.jsonOut
	interactive := !*yes && !asJSON && *step == 0
	w := &setup.Wizard{DataDir: dir, Listen: *listen, Mode: *mode, Domain: *domain, Email: *email,
		RelayInvite: *invite, Staging: *staging, VerifyTimeout: *timeout}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	report := func(r setup.StepResult) bool {
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(r)
			return r.OK
		}
		if !r.OK {
			fmt.Fprintf(os.Stderr, "✗ %d/%d %s: %s\n", r.Step, len(setup.StepNames), r.Name, r.Error)
			return false
		}
		fmt.Printf("✓ %d/%d %s: %s\n", r.Step, len(setup.StepNames), r.Name, r.Message)
		return true
	}
	run := func(n int) setup.StepResult {
		switch n {
		case 1:
			return w.StepInit()
		case 2:
			return w.StepReachability(ctx)
		case 3:
			return w.StepService()
		case 4:
			return w.StepVerify(ctx)
		default:
			return w.StepInvite()
		}
	}
	if *step != 0 {
		if *step < 1 || *step > len(setup.StepNames) {
			return fatal(fmt.Errorf("--step must be 1-%d", len(setup.StepNames)))
		}
		if !report(run(*step)) {
			return 1
		}
		return 0
	}
	if !asJSON {
		fmt.Printf("nubilo setup — server data dir %s\n\n", dir)
	}
	if !report(run(1)) {
		return 1
	}

	if interactive && w.Mode == "" {
		fmt.Println("\nHow will phones reach this server from outside your home?")
		fmt.Println("  1) relay   — a small VPS runs `nubilo relay`; nothing opens on your router (recommended)")
		fmt.Println("  2) proxy   — `nubilo relay --behind-proxy` on a host like exe.dev; its proxy holds the certificate and can read traffic")
		fmt.Println("  3) direct  — this host is reachable on ports 443/80 (port forward or VPS)")
		fmt.Println("  4) lan     — home network / Tailscale only, self-signed certificate")
		switch prompt("choice [1]: ") {
		case "2", "proxy":
			w.Mode = setup.ModeProxy
		case "3", "direct":
			w.Mode = setup.ModeDirect
		case "4", "lan":
			w.Mode = setup.ModeLAN
		default:
			w.Mode = setup.ModeRelay
		}
	}
	if w.Mode == "" {
		w.Mode = setup.ModeLAN
	}
	if interactive && w.Mode != setup.ModeLAN {
		if w.Domain == "" {
			if w.Mode == setup.ModeProxy {
				w.Domain = prompt("public name the proxy serves (e.g. nubilo.exe.xyz): ")
			} else {
				w.Domain = prompt("public domain (DNS A record → the relay VPS or this host): ")
			}
		}
		if w.Email == "" && w.Mode != setup.ModeProxy {
			w.Email = prompt("email for Let's Encrypt expiry notices (optional): ")
		}
		if (w.Mode == setup.ModeRelay || w.Mode == setup.ModeProxy) && w.RelayInvite == "" {
			w.RelayInvite = prompt("relay invite (from `nubilo relay init` on the relay host): ")
		}
	}
	if !report(run(2)) {
		return 1
	}

	if !*noBackup && !asJSON {
		do := *yes
		if interactive {
			fmt.Print("enable encrypted auto-backup? [Y/n] ")
			do = readYesDefaultYes()
		}
		if do {
			if err := enableBackupPrint(dir); err != nil {
				return fatal(err)
			}
		}
	}
	if !*noService {
		do := true
		if interactive {
			fmt.Print("install always-on server service? [Y/n] ")
			do = readYesDefaultYes()
		}
		if do {
			if !report(run(3)) {
				return 1
			}
			if runtime.GOOS == "linux" && !asJSON {
				fmt.Fprintln(os.Stderr, "note: loginctl enable-linger $USER so the service survives logout")
			}
			if !asJSON {
				fmt.Println("waiting for the server to answer (the first certificate can take a minute)…")
			}
			report(run(4))
		}
	}

	r := run(5)
	if !report(r) {
		return 1
	}
	if !asJSON {
		if t, _ := r.Data["qr_text"].(string); t != "" {
			fmt.Print("\n" + t)
		}
		fmt.Printf("Open the Nubilo app and scan the code, or open %v on the phone.\n", r.Data["url"])
		fmt.Printf("This admin invite works once and expires in 24 hours. More invites: nubilo users invite\n\n")
		rep, err := doctor.Server(context.Background(), dir, doctor.Options{})
		if err == nil {
			fmt.Print(doctor.FormatHuman(rep))
		}
	}
	return 0
}

var stdin = bufio.NewReader(os.Stdin)

func prompt(label string) string {
	fmt.Print(label)
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

func enableBackupPrint(dir string) error {
	pass, passFile, err := setup.EnableAutoBackup(dir)
	if err != nil {
		return err
	}
	fmt.Print(setup.PrintPassphraseOnce(pass, passFile))
	return nil
}

func runAgentSetup(g global, insecure bool, args []string) int {
	fs := flag.NewFlagSet("agent-setup", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "non-interactive where possible")
	server := fs.String("server", "", "server URL for pairing")
	code := fs.String("code", "", "pairing code")
	name := fs.String("name", "", "device name")
	noService := fs.Bool("no-service", false, "skip LaunchAgent install")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "nubilo agent setup requires macOS")
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	if g.dataDir == "" {
		// Prefer ~/.nubilo-agent for agent setup when default would be ~/.nubilo
		home, _ := os.UserHomeDir()
		if home != "" {
			cand := filepath.Join(home, ".nubilo-agent")
			if !isServerDataDir(cand) {
				dir = cand
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fatal(err)
	}
	fmt.Printf("nubilo agent setup — data dir %s\n", dir)

	info, err := agent.ReadPairingInfo(dir)
	if err != nil {
		return fatal(err)
	}
	if !info.Paired {
		srv, cde, nam := *server, *code, *name
		if srv == "" || cde == "" || nam == "" {
			if *yes {
				fmt.Fprintln(os.Stderr, "pairing required: pass --server --code --name (or run interactively)")
				return 2
			}
			r := stdin
			if srv == "" {
				fmt.Print("server URL: ")
				srv, _ = r.ReadString('\n')
				srv = strings.TrimSpace(srv)
			}
			if cde == "" {
				fmt.Print("pairing code: ")
				cde, _ = r.ReadString('\n')
				cde = strings.TrimSpace(cde)
			}
			if nam == "" {
				fmt.Print("device name [this Mac]: ")
				nam, _ = r.ReadString('\n')
				nam = strings.TrimSpace(nam)
				if nam == "" {
					nam = "this Mac"
				}
			}
		}
		id, err := agent.PairWithServer(dir, srv, cde, nam, insecure)
		if err != nil {
			return fatal(err)
		}
		fmt.Printf("paired as %s (signing key in Keychain)\n", id)
	} else {
		fmt.Printf("already paired as %s → %s\n", info.Name, info.Server)
		// Migrate file key to Keychain if present.
		if _, err := agent.LoadDeviceKey(dir); err != nil {
			fmt.Fprintf(os.Stderr, "device key: %v\n", err)
		}
	}

	if !*noService {
		doInstall := *yes
		if !*yes {
			fmt.Print("install LaunchAgent (always-on)? [Y/n] ")
			doInstall = readYesDefaultYes()
		}
		if doInstall {
			path, err := setup.InstallAgentService(dir, insecure)
			if err != nil {
				fmt.Fprintf(os.Stderr, "service install: %v\n", err)
			} else {
				fmt.Printf("LaunchAgent installed: %s\n", path)
			}
		}
	}

	if !*yes {
		fmt.Print("authorize Photos Full Access now? [y/N] ")
		if readYesDefaultNo() {
			_ = agentAuthorize()
		}
	}

	fmt.Println("Select calendars/reminders/photos in: nubilo agent ui")
	fmt.Println("Then: nubilo doctor --agent --data-dir " + dir)
	rep, err := doctor.Agent(dir)
	if err == nil {
		fmt.Print(doctor.FormatHuman(rep))
	}
	return 0
}

func readYesDefaultYes() bool {
	line, _ := stdin.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}

func readYesDefaultNo() bool {
	line, _ := stdin.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}
