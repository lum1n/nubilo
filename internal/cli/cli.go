package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"nubilo/internal/agent"
	"nubilo/internal/app"
	"nubilo/internal/backup"
	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/integrity"
	"nubilo/internal/photos"
	"nubilo/internal/server"
	"nubilo/internal/service"
	"nubilo/internal/version"
)

func Main(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	// Launched from Nubilo.app via `open … --args --authorize-photos`
	if args[0] == "--authorize-photos" {
		return runAuthorizePhotosPrompt()
	}
	g, rest := parseGlobal(args)
	cmd := rest[0]
	rest = rest[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	case "version":
		fmt.Println(version.String)
		return 0
	case "init":
		return runInit(g, rest)
	case "server":
		return runServer(g, rest)
	case "agent":
		return runAgent(g, rest)
	case "client":
		fmt.Fprintln(os.Stderr, "generic file client is not implemented until after the sync engine review (use pair + /sync/v1)")
		return 2
	case "status":
		return runStatus(g)
	case "pair":
		return runPair(g, rest)
	case "tls":
		return runTLS(g, rest)
	case "devices":
		return runDevices(g, rest)
	case "verify":
		return runVerify(g, rest)
	case "gc":
		return runGC(g, rest)
	case "backup":
		return runBackup(g, rest)
	case "restore":
		return runRestore(g, rest)
	case "logs":
		fmt.Fprintln(os.Stderr, "structured JSON logs are written to stderr; persist them with systemd/journald.")
		return 0
	case "files":
		return runFiles(g, rest)
	case "calendars":
		return runCalendars(g, rest)
	case "contacts":
		return runContacts(g, rest)
	case "photos":
		return runPhotos(g, rest)
	case "ui":
		return runUI(g, rest)
	case "sync":
		fmt.Fprintf(os.Stderr, "%s: adapter not implemented until a later phase (see IMPLEMENTATION.md)\n", cmd)
		return 2
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage(os.Stderr)
		return 2
	}
}

type global struct {
	dataDir string
	jsonOut bool
	verbose bool
}

func parseGlobal(args []string) (global, []string) {
	var g global
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest = append(rest, args[i+1:]...)
			return g, rest
		case a == "--data-dir" && i+1 < len(args):
			g.dataDir = args[i+1]
			i++
		case strings.HasPrefix(a, "--data-dir="):
			g.dataDir = strings.TrimPrefix(a, "--data-dir=")
		case a == "--json":
			g.jsonOut = true
		case a == "--verbose" || a == "-v":
			g.verbose = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		return g, []string{"help"}
	}
	return g, rest
}

func usage(w io.Writer) {
	fmt.Fprint(w, `nubilo — personal cloud (single binary)

Usage:
  nubilo init [--data-dir DIR] [--listen ADDR]
  nubilo server [--data-dir DIR]
  nubilo server install [--data-dir DIR]
  nubilo server uninstall
  nubilo server service
  nubilo ui [--listen ADDR] [--open]
  nubilo agent [--data-dir DIR] [--insecure] [--interval SECONDS]
  nubilo agent install [--data-dir DIR] [--insecure]
  nubilo agent uninstall
  nubilo agent service
  nubilo agent ui [--listen ADDR] [--open]
  nubilo agent calendars
  nubilo agent select ID
  nubilo agent unselect ID
  nubilo agent reminder-lists
  nubilo agent select-reminder ID
  nubilo agent unselect-reminder ID
  nubilo agent contacts on|off
  nubilo agent photos on|off
  nubilo agent photos source all|albums|dates
  nubilo agent photos select ALBUM_ID
  nubilo agent albums
  nubilo agent authorize
  nubilo agent files on|off
  nubilo agent files list
  nubilo agent files add PATH [NAME]
  nubilo agent files remove PATH
  nubilo status [--json]
  nubilo pair [--wait] [--role agent|client]
  nubilo pair --server URL --code XXXXX-XXXXX --name NAME [--insecure]
  nubilo tls [--listen ADDR] [--hosts HOSTS]   # optional: regenerate auto cert
  nubilo devices [list]
  nubilo devices revoke <id>
  nubilo devices rename <id> <name>
  nubilo devices enroll --name NAME --pubkey FILE [--role agent]
  nubilo devices rotate <id> --pubkey FILE
  nubilo devices password --name NAME [--scope webdav|caldav|carddav|photos|all]
  nubilo files [list]
  nubilo files mkdir NAME
  nubilo calendars [list]
  nubilo calendars create NAME
  nubilo contacts [list]
  nubilo contacts create NAME
  nubilo photos [list]
  nubilo verify [--repair]
  nubilo gc [--apply]
  nubilo backup --out FILE --passphrase-file FILE [--verify]
  nubilo restore --in FILE --dest DIR --passphrase-file FILE

Global flags: --data-dir, --json, --verbose
`)
}

func runInit(g global, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	listen := fs.String("listen", config.DefaultListen, "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	if err := app.Init(dir, *listen); err != nil {
		return fatal(err)
	}
	fmt.Printf("initialized %s\n", dir)
	if *listen == config.DefaultListen {
		fmt.Fprintln(os.Stderr, "listening on loopback only; for a Mac/phone set listen to 0.0.0.0:8443 in config.json (TLS is created automatically)")
	}
	return 0
}

func runTLS(g global, args []string) int {
	fs := flag.NewFlagSet("tls", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:8443", "listen address written to config.json")
	extra := fs.String("hosts", "", "comma-separated extra SAN DNS names or IPs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	hosts := ncrypto.LocalListenHosts()
	if *extra != "" {
		for _, h := range strings.Split(*extra, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				hosts = append(hosts, h)
			}
		}
	}
	p := config.Paths(dir)
	cert := filepath.Join(dir, "tls.crt")
	key := filepath.Join(dir, "tls.key")
	if err := ncrypto.GenerateTLS(cert, key, hosts, 0); err != nil {
		return fatal(err)
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		return fatal(err)
	}
	cfg.DataDir = dir
	cfg.Listen = *listen
	cfg.TLS.Cert = cert
	cfg.TLS.Key = key
	if err := cfg.Validate(); err != nil {
		return fatal(err)
	}
	if err := cfg.Save(p.Config); err != nil {
		return fatal(err)
	}
	fmt.Printf("wrote %s\nwrote %s\nlisten %s\ncertificate SAN: %s\nrestart the server to apply: nubilo server --data-dir %s\n",
		cert, key, *listen, strings.Join(hosts, ", "), dir)
	return 0
}

func runServer(g global, args []string) int {
	sub := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
	}
	switch sub {
	case "install":
		return runServiceInstall(g, service.KindServer, false)
	case "uninstall":
		return runServiceUninstall(service.KindServer)
	case "service":
		return runServiceStatus(service.KindServer)
	case "run":
		// continue below
	default:
		fmt.Fprintf(os.Stderr, "unknown server command %q\n", sub)
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ab := &app.AutoBackup{RT: rt, Log: rt.Log}
	ab.Start()
	defer ab.Stop()
	srv := server.New(rt.Cfg, rt.Store, rt.IDs, rt.Auth, rt.Engine, rt.Audit, rt.Log, rt.ServerPub)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shctx)
		return 0
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fatal(err)
		}
	}
	return 0
}

func runServiceInstall(g global, kind service.Kind, insecure bool) int {
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fatal(err)
	}
	exe, err := service.ResolveExe()
	if err != nil {
		return fatal(err)
	}
	if kind == service.KindAgent {
		appPath, err := agent.InstallAppBundle()
		if err != nil {
			return fatal(err)
		}
		exe = filepath.Join(appPath, "Contents", "MacOS", "nubilo")
	}
	path, err := service.Install(service.Spec{
		Kind:     kind,
		Exe:      exe,
		DataDir:  dir,
		Insecure: insecure,
	})
	if err != nil {
		return fatal(err)
	}
	fmt.Printf("installed %s service\n  unit: %s\n  binary: %s\n  data-dir: %s\n  log: %s\n",
		kind.String(), path, exe, dir, service.LogPath(dir, kind))
	if kind == service.KindServer && runtime.GOOS == "linux" {
		fmt.Fprintln(os.Stderr, "note: systemd --user stops on logout unless you run: loginctl enable-linger $USER")
	}
	return 0
}

func runServiceUninstall(kind service.Kind) int {
	if err := service.Uninstall(kind); err != nil {
		return fatal(err)
	}
	fmt.Printf("uninstalled %s service\n", kind.String())
	return 0
}

func runServiceStatus(kind service.Kind) int {
	st, err := service.Status(kind)
	if err != nil && !st.Installed {
		return fatal(err)
	}
	fmt.Printf("%s service\n", kind.String())
	fmt.Printf("  installed: %v\n", st.Installed)
	fmt.Printf("  loaded: %v\n", st.Loaded)
	fmt.Printf("  path: %s\n", st.Path)
	fmt.Printf("  detail: %s\n", st.Detail)
	return 0
}

func runAgent(g global, args []string) int {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "nubilo agent requires macOS (EventKit, Contacts, and PhotoKit)")
		return 2
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification (self-signed)")
	interval := fs.Int("interval", 0, "sync interval seconds (overrides agent.json for this run)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	sub := "run"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fatal(err)
	}
	paths := config.Paths(dir)
	sel, err := agent.LoadSelection(paths.AgentJSON)
	if err != nil {
		return fatal(err)
	}
	if *interval > 0 {
		sel.IntervalSeconds = *interval
	}
	switch sub {
	case "calendars":
		return agentListCalendars(sel)
	case "select":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent select ID")
			return 2
		}
		return agentSelect(paths.AgentJSON, sel, rest[0], true)
	case "unselect":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent unselect ID")
			return 2
		}
		return agentSelect(paths.AgentJSON, sel, rest[0], false)
	case "reminder-lists":
		return agentListReminderLists(sel)
	case "select-reminder":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent select-reminder ID")
			return 2
		}
		return agentSelectReminder(paths.AgentJSON, sel, rest[0], true)
	case "unselect-reminder":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent unselect-reminder ID")
			return 2
		}
		return agentSelectReminder(paths.AgentJSON, sel, rest[0], false)
	case "contacts":
		if len(rest) < 1 || (rest[0] != "on" && rest[0] != "off") {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent contacts on|off")
			return 2
		}
		sel.SyncContacts = rest[0] == "on"
		if err := agent.SaveSelection(paths.AgentJSON, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("contacts sync %s\n", rest[0])
		return 0
	case "photos":
		return agentPhotos(paths.AgentJSON, sel, rest)
	case "albums":
		return agentListAlbums(sel)
	case "authorize":
		return agentAuthorize()
	case "ui":
		return runAgentUI(g, rest)
	case "files":
		return agentFiles(paths.AgentJSON, sel, rest)
	case "install":
		return runServiceInstall(g, service.KindAgent, *insecure)
	case "uninstall":
		return runServiceUninstall(service.KindAgent)
	case "service":
		return runServiceStatus(service.KindAgent)
	case "run":
		return agentRun(dir, paths, sel, *insecure)
	default:
		fmt.Fprintf(os.Stderr, "unknown agent command %q\n", sub)
		return 2
	}
}

func runAuthorizePhotosPrompt() int {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "authorize-photos requires macOS")
		return 2
	}
	status, err := agent.RequestPhotosAccess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "photos: %v (status=%s)\n", err, status)
		_ = agent.OpenPhotosPrivacySettings()
		return 1
	}
	fmt.Printf("photos access: %s\n", status)
	if status == "limited" {
		fmt.Println("Limited access only syncs the photos you selected in the system picker.")
		fmt.Println("For a full album, choose Allow Full Access (System Settings → Privacy & Security → Photos → Nubilo).")
		_ = agent.OpenPhotosPrivacySettings()
		return 0
	}
	return 0
}

func agentAuthorize() int {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "nubilo agent authorize requires macOS")
		return 2
	}
	fmt.Println("Installing ~/Applications/Nubilo.app and requesting Photos access…")
	fmt.Println("In the dialog, choose Allow Full Access (not Limited / Selected Photos).")
	status, err := agent.AuthorizePhotosViaApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "authorize via app failed (%v); trying in-process request…\n", err)
		status, err = agent.RequestPhotosAccess()
		if err != nil {
			return fatal(err)
		}
	}
	fmt.Printf("photos access: %s\n", status)
	if status == "limited" {
		fmt.Println("WARNING: Limited Photos access — album sync will only include the few photos you allowed.")
		fmt.Println("Open System Settings → Privacy & Security → Photos → Nubilo → Full Access, then re-run the agent.")
		_ = agent.OpenPhotosPrivacySettings()
		return 0
	}
	if status != "authorized" {
		fmt.Println("Still not authorized. Check System Settings → Privacy & Security → Photos.")
		_ = agent.OpenPhotosPrivacySettings()
		return 1
	}
	fmt.Println("Full Photos access granted. Re-run: nubilo agent --data-dir ~/.nubilo-agent")
	return 0
}

func agentListAlbums(sel agent.Selection) int {
	status := agent.PhotosAuthStatus()
	if status == "limited" {
		fmt.Fprintf(os.Stderr, "warning: Photos access is Limited — counts below are only the photos TCC allows (run: nubilo agent authorize)\n")
	}
	libraryCount, albums, err := agent.PlatformAlbumList()
	if err != nil {
		return fatal(err)
	}
	chosen := map[string]bool{}
	for _, id := range sel.Photos.Albums {
		chosen[id] = true
	}
	fmt.Printf("photos access: %s\n", status)
	fmt.Printf("library assets: %d\n", libraryCount)
	if len(albums) == 0 {
		fmt.Println("no PhotoKit albums/people (run: nubilo agent authorize)")
		return 0
	}
	fmt.Println("tip: People & Pets entries are kind=person|pet and use ids like person:… — select those for full pet/person sets")
	for _, a := range albums {
		mark := " "
		if chosen[a.ID] {
			mark = "*"
		}
		kind := a.Kind
		if kind == "" {
			kind = "user"
		}
		fmt.Printf("%s  %s  [%s]  %s  (%d)\n", mark, a.ID, kind, a.Title, a.Count)
	}
	return 0
}

func agentPhotos(path string, sel agent.Selection, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: nubilo agent photos on|off|source|select|unselect")
		return 2
	}
	switch args[0] {
	case "on", "off":
		sel.Photos.Enabled = args[0] == "on"
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("photos sync %s\n", args[0])
		return 0
	case "source":
		if len(args) < 2 || (args[1] != "all" && args[1] != "albums" && args[1] != "dates") {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent photos source all|albums|dates")
			return 2
		}
		sel.Photos.Source = args[1]
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("photos source %s\n", args[1])
		return 0
	case "select":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent photos select ALBUM_ID")
			fmt.Fprintln(os.Stderr, "  quote ids that contain / or start with person:")
			fmt.Fprintln(os.Stderr, `  e.g. nubilo agent photos select 'person:XXXX'`)
			return 2
		}
		sel.SelectAlbum(args[1])
		sel.Photos.Source = "albums"
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("selected album %s\n", args[1])
		return 0
	case "unselect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent photos unselect ALBUM_ID")
			return 2
		}
		sel.UnselectAlbum(args[1])
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("unselected album %s\n", args[1])
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown photos command %q\n", args[0])
		return 2
	}
}

func agentFiles(path string, sel agent.Selection, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: nubilo agent files on|off|list|add|remove")
		return 2
	}
	switch args[0] {
	case "on", "off":
		sel.Files.Enabled = args[0] == "on"
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("files sync %s\n", args[0])
		return 0
	case "list":
		if len(sel.Files.Folders) == 0 {
			fmt.Println("(no folders)")
			return 0
		}
		for _, f := range sel.Files.Folders {
			name := f.Name
			if name == "" {
				name = filepath.Base(f.Path)
			}
			fmt.Printf("%s  %s\n", f.Path, name)
		}
		return 0
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent files add PATH [NAME]")
			return 2
		}
		abs, err := filepath.Abs(args[1])
		if err != nil {
			return fatal(err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return fatal(err)
		}
		if !st.IsDir() {
			return fatal(fmt.Errorf("%s is not a directory", abs))
		}
		name := ""
		if len(args) >= 3 {
			name = strings.Join(args[2:], " ")
		}
		sel.AddFileFolder(abs, name)
		sel.Files.Enabled = true
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("added folder %s\n", abs)
		return 0
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: nubilo agent files remove PATH")
			return 2
		}
		abs, err := filepath.Abs(args[1])
		if err != nil {
			return fatal(err)
		}
		sel.RemoveFileFolder(abs)
		// also try the raw path in case it was stored differently
		sel.RemoveFileFolder(args[1])
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("removed folder %s\n", abs)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown files command %q\n", args[0])
		return 2
	}
}

func agentListCalendars(sel agent.Selection) int {
	cals, err := agent.PlatformCalendars()
	if err != nil {
		return fatal(err)
	}
	chosen := map[string]bool{}
	for _, c := range sel.Calendars {
		chosen[c.LocalID] = true
	}
	if len(cals) == 0 {
		fmt.Println("no EventKit calendars (grant Calendar access to Terminal / nubilo)")
		return 0
	}
	for _, c := range cals {
		mark := " "
		if chosen[c.ID] {
			mark = "*"
		}
		fmt.Printf("%s  %s  %s\n", mark, c.ID, c.Title)
	}
	return 0
}

func agentSelect(path string, sel agent.Selection, id string, on bool) int {
	if !on {
		sel.UnselectCalendar(id)
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("unselected %s\n", id)
		return 0
	}
	cals, err := agent.PlatformCalendars()
	if err != nil {
		return fatal(err)
	}
	title := ""
	found := false
	for _, c := range cals {
		if c.ID == id {
			title = c.Title
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "unknown EventKit calendar %q (run nubilo agent calendars)\n", id)
		return 2
	}
	sel.SelectCalendar(id, title)
	if err := agent.SaveSelection(path, sel); err != nil {
		return fatal(err)
	}
	fmt.Printf("selected %s (%s)\n", id, title)
	fmt.Println("restart the running agent (or wait for the next sync) so this calendar is created on the server")
	return 0
}

func agentListReminderLists(sel agent.Selection) int {
	lists, err := agent.PlatformReminderLists()
	if err != nil {
		return fatal(err)
	}
	chosen := map[string]bool{}
	for _, c := range sel.Reminders {
		chosen[c.LocalID] = true
	}
	if len(lists) == 0 {
		fmt.Println("no EventKit reminder lists (grant Reminders access to Terminal / nubilo)")
		return 0
	}
	for _, c := range lists {
		mark := " "
		if chosen[c.ID] {
			mark = "*"
		}
		fmt.Printf("%s  %s  %s\n", mark, c.ID, c.Title)
	}
	return 0
}

func agentSelectReminder(path string, sel agent.Selection, id string, on bool) int {
	if !on {
		sel.UnselectReminder(id)
		if err := agent.SaveSelection(path, sel); err != nil {
			return fatal(err)
		}
		fmt.Printf("unselected reminder list %s\n", id)
		return 0
	}
	lists, err := agent.PlatformReminderLists()
	if err != nil {
		return fatal(err)
	}
	title := ""
	found := false
	for _, c := range lists {
		if c.ID == id {
			title = c.Title
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "unknown EventKit reminder list %q (run nubilo agent reminder-lists)\n", id)
		return 2
	}
	sel.SelectReminder(id, title)
	if err := agent.SaveSelection(path, sel); err != nil {
		return fatal(err)
	}
	fmt.Printf("selected reminder list %s (%s)\n", id, title)
	fmt.Println("restart the running agent (or wait for the next sync) so this list is created on the server")
	return 0
}

func agentRun(dir string, paths config.PathsSet, sel agent.Selection, insecure bool) int {
	client, err := agent.LoadPairedClient(dir, insecure)
	if err != nil {
		if errors.Is(err, agent.ErrNotPaired) {
			fmt.Fprintln(os.Stderr, "nubilo agent: device is not paired")
			fmt.Fprintln(os.Stderr, "on the server: nubilo pair --role agent")
			fmt.Fprintln(os.Stderr, "on this Mac:   nubilo pair --server URL --code … --name \"this Mac\" [--insecure]")
			return 2
		}
		return fatal(err)
	}
	if insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS certificate verification disabled (--insecure)")
	}
	mp, err := agent.OpenMap(paths.AgentDB)
	if err != nil {
		return fatal(err)
	}
	defer mp.Close()
	cal, book, pics, rems, err := agent.OpenPlatform(sel)
	if err != nil {
		return fatal(err)
	}
	a := &agent.Agent{
		Client:    client,
		Map:       mp,
		Sel:       sel,
		SelPath:   paths.AgentJSON,
		Cal:       cal,
		Reminders: rems,
		Contacts:  book,
		Photos:    pics,
		Files:     agent.OpenFiles(),
		Log:       slog.Default(),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Printf("agent running interval=%ds window=%dd calendars=%d reminders=%d contacts=%v photos=%v files=%v\n",
		sel.IntervalSeconds, sel.WindowDays, len(sel.Calendars), len(sel.Reminders), sel.SyncContacts, sel.Photos.Enabled, sel.Files.Enabled)
	for _, c := range sel.Calendars {
		fmt.Printf("  calendar %s (%s)\n", c.LocalID, c.Title)
	}
	for _, c := range sel.Reminders {
		fmt.Printf("  reminders %s (%s)\n", c.LocalID, c.Title)
	}
	for _, f := range sel.Files.Folders {
		name := f.Name
		if name == "" {
			name = filepath.Base(f.Path)
		}
		fmt.Printf("  files %s (%s)\n", f.Path, name)
	}
	if len(sel.Calendars) == 0 && len(sel.Reminders) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no calendars or reminder lists selected; iPhone CalDAV will stay empty")
		fmt.Fprintln(os.Stderr, "  nubilo agent --data-dir "+dir+" calendars")
		fmt.Fprintln(os.Stderr, "  nubilo agent --data-dir "+dir+" select <eventkit-id>")
		fmt.Fprintln(os.Stderr, "  nubilo agent --data-dir "+dir+" reminder-lists")
		fmt.Fprintln(os.Stderr, "  nubilo agent --data-dir "+dir+" select-reminder <eventkit-id>")
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fatal(err)
	}
	return 0
}

func runStatus(g global) int {
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	head, err := rt.Engine.HeadSeq(context.Background())
	if err != nil {
		return fatal(err)
	}
	devs, err := rt.IDs.List(context.Background())
	if err != nil {
		return fatal(err)
	}
	active := 0
	for _, d := range devs {
		if !d.Revoked() {
			active++
		}
	}
	out := map[string]any{
		"data_dir": dir,
		"listen":   rt.Cfg.Listen,
		"head_seq": head,
		"devices":  active,
		"version":  version.String,
	}
	return printOut(g, out, fmt.Sprintf("nubilo %s  listen=%s  head_seq=%d  devices=%d\n", version.String, rt.Cfg.Listen, head, active))
}

func runPair(g global, args []string) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	serverURL := fs.String("server", "", "server URL (client mode)")
	code := fs.String("code", "", "pairing code")
	name := fs.String("name", "", "device name")
	role := fs.String("role", "client", "role for server-issued pairing")
	wait := fs.Bool("wait", true, "wait for client to complete (server mode)")
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification (self-signed)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *serverURL != "" {
		return runPairClient(g, *serverURL, *code, *name, *insecure)
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	r := identity.Role(*role)
	raw, sess, err := rt.IDs.StartPairing(context.Background(), r)
	if err != nil {
		return fatal(err)
	}
	fmt.Printf("Pairing code: %s\nExpires: %s\nRole: %s\n", ncrypto.FormatPairingCode(raw), time.UnixMilli(sess.ExpiresAt).Format(time.RFC3339), r)
	if !*wait {
		return 0
	}
	fmt.Println("Waiting for device…")
	deadline := time.UnixMilli(sess.ExpiresAt)
	for time.Now().Before(deadline) {
		var completed sql.NullInt64
		var deviceID sql.NullString
		err := rt.Store.DB.QueryRow(`SELECT completed_at, device_id FROM pairing_sessions WHERE id=?`, sess.ID).Scan(&completed, &deviceID)
		if err == nil && completed.Valid {
			fmt.Printf("Paired device %s\n", deviceID.String)
			return 0
		}
		time.Sleep(400 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "pairing expired")
	return 1
}

func runPairClient(g global, serverURL, code, name string, insecure bool) int {
	if code == "" || name == "" {
		fmt.Fprintln(os.Stderr, "pair client requires --server, --code, and --name")
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	if insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS certificate verification disabled (--insecure)")
	}
	id, err := agent.PairWithServer(dir, serverURL, code, name, insecure)
	if err != nil {
		return fatal(err)
	}
	fmt.Printf("paired as %s\n", id)
	return 0
}

func runDevices(g global, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	switch sub {
	case "list":
		list, err := rt.IDs.List(ctx)
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(list)
		}
		for _, d := range list {
			rev := ""
			if d.Revoked() {
				rev = " REVOKED"
			}
			fmt.Printf("%s  %s  %s%s\n", d.ID, d.Role, d.Name, rev)
		}
		return 0
	case "revoke":
		if len(args) < 1 {
			return fatal(fmt.Errorf("devices revoke <id>"))
		}
		if err := rt.IDs.Revoke(ctx, args[0]); err != nil {
			return fatal(err)
		}
		fmt.Println("revoked", args[0])
		return 0
	case "rename":
		if len(args) < 2 {
			return fatal(fmt.Errorf("devices rename <id> <name>"))
		}
		if err := rt.IDs.Rename(ctx, args[0], strings.Join(args[1:], " ")); err != nil {
			return fatal(err)
		}
		fmt.Println("renamed", args[0])
		return 0
	case "enroll":
		fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
		name := fs.String("name", "", "device name")
		pubf := fs.String("pubkey", "", "public key file (raw 32 bytes or base64)")
		role := fs.String("role", "client", "role")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if *name == "" || *pubf == "" {
			return fatal(fmt.Errorf("enroll requires --name and --pubkey"))
		}
		b, err := os.ReadFile(*pubf)
		if err != nil {
			return fatal(err)
		}
		pub, err := parsePub(b)
		if err != nil {
			return fatal(err)
		}
		dev, err := rt.IDs.Enroll(ctx, *name, pub, identity.Role(*role))
		if err != nil {
			return fatal(err)
		}
		fmt.Println(dev.ID)
		return 0
	case "rotate":
		fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
		pubf := fs.String("pubkey", "", "new public key file")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() < 1 || *pubf == "" {
			return fatal(fmt.Errorf("devices rotate <id> --pubkey FILE"))
		}
		b, err := os.ReadFile(*pubf)
		if err != nil {
			return fatal(err)
		}
		pub, err := parsePub(b)
		if err != nil {
			return fatal(err)
		}
		if err := rt.IDs.RotatePublicKey(ctx, fs.Arg(0), pub); err != nil {
			return fatal(err)
		}
		fmt.Println("rotated", fs.Arg(0))
		return 0
	case "password":
		fs := flag.NewFlagSet("password", flag.ContinueOnError)
		name := fs.String("name", "", "device name")
		scope := fs.String("scope", "webdav", "webdav, caldav, carddav, photos, or all")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if *name == "" {
			return fatal(fmt.Errorf("devices password requires --name"))
		}
		dev, pass, err := rt.IDs.CreateDAVDevice(ctx, *name, *scope)
		if err != nil {
			return fatal(err)
		}
		scheme := "http://"
		if !rt.Cfg.Loopback() {
			scheme = "https://"
		}
		base := scheme + rt.Cfg.Listen
		fmt.Printf("username: %s\npassword: %s\n", dev.ID, pass)
		if dev.Permissions.HasProtocol("webdav") {
			fmt.Printf("webdav:   %s/dav/\n", base)
		}
		if dev.Permissions.HasProtocol("caldav") {
			fmt.Printf("caldav:   %s/caldav/\n", base)
		}
		if dev.Permissions.HasProtocol("carddav") {
			fmt.Printf("carddav:  %s/carddav/\n", base)
		}
		if dev.Permissions.HasProtocol("photos") {
			fmt.Printf("photos:   %s/api/v1/photos\n", base)
		}
		fmt.Fprintln(os.Stderr, "the password is shown once; store it in the Apple account password field")
		return 0
	default:
		return fatal(fmt.Errorf("unknown devices subcommand %s", sub))
	}
}

func runVerify(g global, args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	repair := fs.Bool("repair", false, "remove orphan blobs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	issues, err := integrity.Check(context.Background(), rt.Store)
	if err != nil {
		return fatal(err)
	}
	if *repair {
		orphans, refs, err := integrity.Repair(context.Background(), rt.Store)
		if err != nil {
			return fatal(err)
		}
		fmt.Printf("removed %d orphan blobs; repaired %d refcounts\n", orphans, refs)
		issues, err = integrity.Check(context.Background(), rt.Store)
		if err != nil {
			return fatal(err)
		}
	}
	if g.jsonOut {
		return dumpJSON(issues)
	}
	if len(issues) == 0 {
		fmt.Println("ok")
		return 0
	}
	for _, i := range issues {
		fmt.Println(i.String())
	}
	return 1
}

func runGC(g global, args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "delete unreferenced blobs and compact eligible tombstones")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	rep, err := integrity.Collect(context.Background(), rt.Store, *apply)
	if err != nil {
		return fatal(err)
	}
	if g.jsonOut {
		return dumpJSON(rep)
	}
	mode := "dry-run"
	if *apply {
		mode = "applied"
	}
	fmt.Printf("gc %s: min_ack=%d blobs=%d tombstones=%d\n", mode, rep.MinAckSeq, len(rep.UnreferencedBlobs), len(rep.CompactableTombstones))
	if *apply {
		fmt.Printf("removed %d blobs, compacted %d tombstones\n", rep.BlobsRemoved, rep.TombstonesCompacted)
	}
	return 0
}

func runBackup(g global, args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fs.String("out", "", "archive path")
	pf := fs.String("passphrase-file", "", "file containing passphrase")
	verify := fs.Bool("verify", false, "restore into temp dir and verify")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || *pf == "" {
		return fatal(fmt.Errorf("backup requires --out and --passphrase-file"))
	}
	pass, err := os.ReadFile(*pf)
	if err != nil {
		return fatal(err)
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	phrase := strings.TrimSpace(string(pass))
	if err := backup.Create(context.Background(), rt.Store, dir, *out, phrase); err != nil {
		return fatal(err)
	}
	if *verify {
		issues, err := backup.VerifyRestore(context.Background(), *out, phrase, nil)
		if err != nil {
			return fatal(err)
		}
		if len(issues) > 0 {
			return fatal(fmt.Errorf("backup verify found %d issues", len(issues)))
		}
	}
	fmt.Println("wrote", *out)
	return 0
}

func runRestore(g global, args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	in := fs.String("in", "", "archive path")
	dest := fs.String("dest", "", "empty destination data dir")
	pf := fs.String("passphrase-file", "", "passphrase file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *in == "" || *dest == "" || *pf == "" {
		return fatal(fmt.Errorf("restore requires --in, --dest, --passphrase-file"))
	}
	pass, err := os.ReadFile(*pf)
	if err != nil {
		return fatal(err)
	}
	if err := backup.Restore(context.Background(), *in, *dest, strings.TrimSpace(string(pass))); err != nil {
		return fatal(err)
	}
	fmt.Println("restored to", *dest)
	return 0
}

func runFiles(g global, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	switch sub {
	case "list":
		cols, err := rt.Engine.ChildCollections(ctx, "files", "")
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(cols)
		}
		if len(cols) == 0 {
			fmt.Println("(no file collections)")
			return 0
		}
		for _, c := range cols {
			fmt.Printf("%s  %s\n", c.ID, c.Name)
		}
		return 0
	case "mkdir":
		if len(args) < 1 {
			return fatal(fmt.Errorf("files mkdir NAME"))
		}
		c, err := rt.Engine.CreateCollection(ctx, "files", strings.Join(args, " "), "", nil)
		if err != nil {
			return fatal(err)
		}
		fmt.Println(c.ID)
		return 0
	default:
		return fatal(fmt.Errorf("unknown files subcommand %s", sub))
	}
}

func runCalendars(g global, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	switch sub {
	case "list":
		cols, err := rt.Engine.ChildCollections(ctx, "calendar", "")
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(cols)
		}
		if len(cols) == 0 {
			fmt.Println("(no calendars)")
			return 0
		}
		for _, c := range cols {
			fmt.Printf("%s  %s\n", c.ID, c.Name)
		}
		return 0
	case "create":
		if len(args) < 1 {
			return fatal(fmt.Errorf("calendars create NAME"))
		}
		c, err := rt.Engine.CreateCollection(ctx, "calendar", strings.Join(args, " "), "", nil)
		if err != nil {
			return fatal(err)
		}
		fmt.Println(c.ID)
		return 0
	default:
		return fatal(fmt.Errorf("unknown calendars subcommand %s", sub))
	}
}

func runContacts(g global, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	switch sub {
	case "list":
		cols, err := rt.Engine.ChildCollections(ctx, "addressbook", "")
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(cols)
		}
		if len(cols) == 0 {
			fmt.Println("(no address books)")
			return 0
		}
		for _, c := range cols {
			fmt.Printf("%s  %s\n", c.ID, c.Name)
		}
		return 0
	case "create":
		if len(args) < 1 {
			return fatal(fmt.Errorf("contacts create NAME"))
		}
		c, err := rt.Engine.CreateCollection(ctx, "addressbook", strings.Join(args, " "), "", nil)
		if err != nil {
			return fatal(err)
		}
		fmt.Println(c.ID)
		return 0
	default:
		return fatal(fmt.Errorf("unknown contacts subcommand %s", sub))
	}
}

func runPhotos(g global, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	switch sub {
	case "list":
		svc := photos.Service{Engine: rt.Engine, Store: rt.Store, Opt: photos.Options{
			StripGPSFromDerivatives: rt.Cfg.Photos.StripGPSFromDerivatives,
			PerceptualHash:          rt.Cfg.Photos.PerceptualHash,
			ThumbMaxPx:              rt.Cfg.Photos.ThumbMaxPx,
			PreviewMaxPx:            rt.Cfg.Photos.PreviewMaxPx,
		}}
		objs, err := svc.List(ctx)
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			out := make([]map[string]any, 0, len(objs))
			for i := range objs {
				m := photos.ParseMeta(objs[i].Metadata)
				row := photos.PublicMeta(m)
				row["id"] = objs[i].ID
				out = append(out, row)
			}
			return dumpJSON(out)
		}
		if len(objs) == 0 {
			fmt.Println("(no photos)")
			return 0
		}
		for _, o := range objs {
			m := photos.ParseMeta(o.Metadata)
			fmt.Printf("%s  %s  %s  %dx%d\n", o.ID, m.Name, m.MIME, m.Width, m.Height)
		}
		return 0
	default:
		return fatal(fmt.Errorf("unknown photos subcommand %s", sub))
	}
}

func printOut(g global, v any, text string) int {
	if g.jsonOut {
		return dumpJSON(v)
	}
	fmt.Print(text)
	return 0
}

func dumpJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return 0
}

func fatal(err error) int {
	fmt.Fprintln(os.Stderr, err)
	return 1
}

func parsePub(b []byte) ([]byte, error) {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 32 {
		return b, nil
	}
	return b64in(string(b))
}
