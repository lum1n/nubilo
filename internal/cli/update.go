package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nubilo/internal/app"
	"nubilo/internal/config"
	"nubilo/internal/relay"
	"nubilo/internal/service"
	"nubilo/internal/update"
	"nubilo/internal/version"
	"nubilo/internal/wiredx"
)

// runUpdate: nubilo update [status|check|install|rollback]. status, check
// and install ask the running server (loopback, admin token), which does
// the work and restarts itself. preflight and guard are run by the updater
// and the service unit.
func runUpdate(g global, args []string) int {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if rdir, ok := relayDataDir(g); ok && sub != "guard" {
		return runRelayUpdate(rdir, sub)
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	switch sub {
	case "preflight":
		// The new binary opening (and migrating) a snapshot of the data.
		rt, err := app.Open(dir)
		if err != nil {
			return fatal(err)
		}
		rt.Close()
		fmt.Println("ok")
		return 0
	case "guard":
		if err := update.Guard(dir, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "update guard:", err)
			return 1
		}
		return 0
	case "rollback":
		if err := update.Rollback(dir, "rolled back by hand", time.Now()); err != nil {
			return fatal(err)
		}
		fmt.Println("The previous binary is back; its data is restored when the server starts.")
		if runtime.GOOS == "linux" {
			if out, err := exec.Command("systemctl", "--user", "restart", service.KindServer.UnitName()).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "restart the server yourself: %v %s\n", err, strings.TrimSpace(string(out)))
				return 1
			}
			fmt.Println("Server restarted.")
		}
		return 0
	case "status", "check", "install":
	default:
		fmt.Fprintf(os.Stderr, "unknown update command %q (status, check, install, rollback)\n", sub)
		return 2
	}

	method, path := http.MethodGet, "/api/v1/admin/update"
	switch sub {
	case "check":
		method, path = http.MethodPost, "/api/v1/admin/update/check"
	case "install":
		method, path = http.MethodPost, "/api/v1/admin/update/install"
	}
	var st update.Status
	if err := localAdmin(dir, method, path, &st); err != nil {
		return fatal(fmt.Errorf("%w (is `nubilo server` running?)", err))
	}
	if g.jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(st)
		return 0
	}
	printUpdateStatus(st)
	if sub == "install" {
		fmt.Println("Installing; the server restarts when it is in place. `nubilo update` shows how it went.")
	}
	return 0
}

func printUpdateStatus(st update.Status) {
	fmt.Printf("current:    %s\n", st.Current)
	switch {
	case st.Available != nil:
		fmt.Printf("available:  %s\n", st.Available.Version)
	case st.CheckedAt > 0:
		fmt.Printf("available:  none (checked %s)\n", time.UnixMilli(st.CheckedAt).Format(time.DateTime))
	default:
		fmt.Println("available:  not checked yet")
	}
	if st.Blocked != "" {
		fmt.Printf("blocked:    %s\n", st.Blocked)
	}
	fmt.Printf("state:      %s\n", st.State)
	if st.Auto {
		fmt.Printf("automatic:  on, next window %s\n", time.UnixMilli(st.NextAutoAt).Format(time.DateTime))
	} else {
		fmt.Println("automatic:  off")
	}
	if l := st.Last; l != nil {
		fmt.Printf("last:       %s → %s %s %s\n", l.From, l.To, l.Result, l.Message)
	}
	if r := st.Relay; r != nil {
		if r.Reachable {
			fmt.Printf("relay:      %s (updated first, with this server)\n", r.Current)
		} else {
			fmt.Printf("relay:      can't be updated from here: %s\n", r.Error)
		}
	}
}

// localAdmin calls the running server's API on loopback with the admin
// token, like `nubilo doctor`.
func localAdmin(dir, method, path string, out any) error {
	cfg, err := config.Load(config.Paths(dir).Config)
	if err != nil {
		return err
	}
	tok, err := os.ReadFile(config.Paths(dir).AdminToken)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "https"
	if cfg.TLS.Cert == "" {
		scheme = "http"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, scheme+"://"+net.JoinHostPort(host, port)+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Nubilo-Admin", strings.TrimSpace(string(tok)))
	c := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback to our own self-signed listener
	}}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// relayDataDir finds a relay's data directory: the one given (or the
// default ~/.nubilo-relay) when it holds relay.json and no home server
// config.
func relayDataDir(g global) (string, bool) {
	dir, err := relayDir(g)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(dir, relay.ConfigFile)); err != nil {
		return "", false
	}
	if g.dataDir == "" {
		// A machine running a home server too: `nubilo update` is about that.
		if home, err := app.ResolveDataDir(""); err == nil {
			if _, err := os.Stat(config.Paths(home).Config); err == nil {
				return "", false
			}
		}
	}
	return dir, true
}

// runRelayUpdate: on a relay, updates are started by its home server, so
// this only reports, and rolls back by hand.
func runRelayUpdate(dir, sub string) int {
	switch sub {
	case "status":
		fmt.Printf("relay:      %s (wired protocol %s)\n", version.String, firstNonEmpty(wiredx.Protocol, "?"))
		if st, err := update.LoadState(dir); err == nil && st != nil {
			at := ""
			if st.FinishedAt > 0 {
				at = time.UnixMilli(st.FinishedAt).Format(time.DateTime)
			}
			fmt.Printf("last:       %s → %s %s %s %s\n", st.From, st.To, st.Phase, at, st.Message)
		}
		fmt.Println("This relay updates when its home server does (first, then the server).")
		fmt.Println("To update now, run `nubilo update install` on the home server, or press Update in the app.")
		return 0
	case "rollback":
		if err := update.Rollback(dir, "rolled back by hand", time.Now()); err != nil {
			return fatal(err)
		}
		fmt.Println("The previous binary is back; its wired state is restored when the relay starts.")
		if runtime.GOOS == "linux" {
			if out, err := exec.Command("systemctl", "--user", "restart", service.KindRelay.UnitName()).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "restart the relay yourself: %v %s\n", err, strings.TrimSpace(string(out)))
				return 1
			}
			fmt.Println("Relay restarted.")
		}
		return 0
	case "check", "install":
		fmt.Fprintln(os.Stderr, "This is a relay: it updates when its home server does. Run `nubilo update "+sub+"` on the home server, or press Update in the app.")
		return 2
	default:
		fmt.Fprintf(os.Stderr, "unknown update command %q (status, rollback)\n", sub)
		return 2
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
