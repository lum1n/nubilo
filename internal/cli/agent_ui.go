package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"nubilo/internal/app"
	"nubilo/internal/ui"
)

func runAgentUI(g global, args []string) int {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "nubilo agent ui requires macOS")
		return 2
	}
	fs := flag.NewFlagSet("agent ui", flag.ContinueOnError)
	listen := fs.String("listen", ui.DefaultAgentListen, "loopback address for the agent UI")
	open := fs.Bool("open", true, "open the UI in a browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fatal(err)
	}
	log := slog.Default()
	if !g.verbose {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	srv, err := ui.NewAgent(dir, *listen, log)
	if err != nil {
		return fatal(err)
	}
	url := srv.SessionURL()
	fmt.Fprintf(os.Stderr, "Nubilo agent UI at %s\n", url)
	fmt.Fprintf(os.Stderr, "data dir: %s\n", dir)
	if *open {
		openBrowser(url)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
