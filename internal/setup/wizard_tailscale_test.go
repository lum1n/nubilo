package setup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nubilo/internal/config"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/setup"
	"nubilo/internal/tailscale"
)

func fakeTailscale(t *testing.T, certDomains string) {
	t.Helper()
	old := tailscale.Run
	t.Cleanup(func() { tailscale.Run = old })
	tailscale.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "status":
			return []byte(`{"BackendState":"Running","CertDomains":[` + certDomains + `],"Self":{"DNSName":"box.tail1234.ts.net.","Online":true}}`), nil
		case "cert":
			return nil, ncrypto.GenerateTLS(args[2], args[4], []string{args[5]}, 0)
		}
		return nil, errors.New("unexpected " + args[0])
	}
}

func TestWizardTailscaleMode(t *testing.T) {
	fakeTailscale(t, `"box.tail1234.ts.net"`)
	dir := t.TempDir()
	w := &setup.Wizard{DataDir: dir, Listen: "0.0.0.0:18446", Mode: setup.ModeTailscale}
	if r := w.StepInit(); !r.OK {
		t.Fatal(r.Error)
	}
	r := w.StepReachability(context.Background())
	if !r.OK {
		t.Fatal(r.Error)
	}
	if !strings.Contains(r.Message, "https://box.tail1234.ts.net:18446") || strings.Contains(r.Message, "no certificate") {
		t.Fatalf("message %q", r.Message)
	}
	cfg, err := config.Load(config.Paths(dir).Config)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Public.Tailscale.Enabled || cfg.Public.Domain != "" || cfg.DeviceBaseURL() != "https://box.tail1234.ts.net:18446" {
		t.Fatalf("public %+v url %s", cfg.Public, cfg.DeviceBaseURL())
	}

	// Switching to LAN mode clears it.
	w.Mode = setup.ModeLAN
	if r := w.StepReachability(context.Background()); !r.OK {
		t.Fatal(r.Error)
	}
	cfg, _ = config.Load(config.Paths(dir).Config)
	if cfg.Public.Tailscale.Enabled || cfg.PublicURL() != "" {
		t.Fatalf("lan mode kept tailscale: %+v", cfg.Public)
	}
}

func TestWizardTailscaleNeedsHTTPSCertificates(t *testing.T) {
	fakeTailscale(t, "")
	dir := t.TempDir()
	w := &setup.Wizard{DataDir: dir, Listen: "0.0.0.0:18447", Mode: setup.ModeTailscale}
	if r := w.StepInit(); !r.OK {
		t.Fatal(r.Error)
	}
	r := w.StepReachability(context.Background())
	if r.OK || !strings.Contains(r.Error, "HTTPS Certificates") {
		t.Fatalf("%+v", r)
	}
}
