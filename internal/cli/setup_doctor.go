package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
	listen := fs.String("listen", "0.0.0.0:8443", "listen address for new installs")
	yes := fs.Bool("yes", false, "non-interactive defaults")
	noService := fs.Bool("no-service", false, "skip always-on service install")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	fmt.Printf("nubilo setup — server data dir %s\n", dir)

	created, err := setup.EnsureServerInitialized(dir, *listen)
	if err != nil {
		return fatal(err)
	}
	if created {
		fmt.Printf("initialized (listen %s)\n", *listen)
	} else {
		fmt.Println("already initialized")
	}

	if !*yes {
		fmt.Print("enable encrypted auto-backup? [Y/n] ")
		if !readYesDefaultYes() {
			fmt.Println("skipped auto-backup — run setup again or enable config.backup manually")
		} else {
			if err := enableBackupPrint(dir); err != nil {
				return fatal(err)
			}
		}
	} else {
		if err := enableBackupPrint(dir); err != nil {
			return fatal(err)
		}
	}

	if !*noService {
		doInstall := *yes
		if !*yes {
			fmt.Print("install always-on server service? [Y/n] ")
			doInstall = readYesDefaultYes()
		}
		if doInstall {
			path, err := setup.InstallServerService(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "service install: %v\n", err)
			} else {
				fmt.Printf("service installed: %s\n", path)
				if runtime.GOOS == "linux" {
					fmt.Fprintln(os.Stderr, "note: loginctl enable-linger $USER so the service survives logout")
				}
			}
		}
	}

	fmt.Print(setup.NextStepsServer(dir))
	fmt.Println()
	rep, err := doctor.Server(context.Background(), dir, doctor.Options{})
	if err == nil {
		fmt.Print(doctor.FormatHuman(rep))
	}
	return 0
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
			r := bufio.NewReader(os.Stdin)
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
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}

func readYesDefaultNo() bool {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}
