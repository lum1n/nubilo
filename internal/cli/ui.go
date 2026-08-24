package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"nubilo/internal/app"
	"nubilo/internal/ui"
)

func runUI(g global, args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	listen := fs.String("listen", ui.DefaultListen, "loopback address for the web UI")
	open := fs.Bool("open", true, "open the UI in a browser")
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
	log := slog.Default()
	if !g.verbose {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	srv, err := ui.New(rt, *listen, log)
	if err != nil {
		return fatal(err)
	}
	url := srv.SessionURL()
	fmt.Fprintf(os.Stderr, "Nubilo UI at %s\n", url)
	fmt.Fprintf(os.Stderr, "Admin token: %s\n", rt.Paths.AdminToken)
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

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	_ = cmd.Start()
}
