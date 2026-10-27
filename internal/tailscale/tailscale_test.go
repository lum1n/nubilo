package tailscale_test

import (
	"context"
	"errors"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/tailscale"
)

// FakeCLI answers `tailscale status --json` and writes a self-signed pair
// for `tailscale cert`.
func fakeCLI(t *testing.T, status string, certErr error) {
	t.Helper()
	old := tailscale.Run
	t.Cleanup(func() { tailscale.Run = old })
	tailscale.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "status":
			return []byte(status), nil
		case "cert":
			if certErr != nil {
				return nil, certErr
			}
			// cert --cert-file C --key-file K NAME
			return nil, ncrypto.GenerateTLS(args[2], args[4], []string{args[5]}, 0)
		}
		return nil, errors.New("unexpected " + args[0])
	}
}

const statusJSON = `{"BackendState":"Running","CertDomains":["box.tail1234.ts.net"],"Self":{"DNSName":"box.tail1234.ts.net.","Online":true}}`

func TestStatus(t *testing.T) {
	fakeCLI(t, statusJSON, nil)
	self, err := tailscale.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if self.Name != "box.tail1234.ts.net" || !self.CertOK || !self.Online {
		t.Fatalf("%+v", self)
	}
	fakeCLI(t, `{"BackendState":"Running","Self":{"DNSName":"box.tail1234.ts.net."}}`, nil)
	if self, _ := tailscale.Status(context.Background()); self.CertOK {
		t.Fatal("CertOK without cert domains")
	}
	fakeCLI(t, `{"BackendState":"NeedsLogin","Self":{"DNSName":""}}`, nil)
	if _, err := tailscale.Status(context.Background()); err == nil {
		t.Fatal("expected an error without a MagicDNS name")
	}
}

func TestCertsRefreshAndGet(t *testing.T) {
	fakeCLI(t, statusJSON, nil)
	c := &tailscale.Certs{Name: "box.tail1234.ts.net", Dir: t.TempDir()}
	if c.Get("box.tail1234.ts.net") != nil || c.Loaded() {
		t.Fatal("certificate before refresh")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Get("BOX.tail1234.ts.net.") == nil {
		t.Fatal("no certificate for the name")
	}
	if c.Get("192.168.1.5") != nil {
		t.Fatal("certificate for another name")
	}

	// A failed renewal keeps the loaded certificate.
	fakeCLI(t, statusJSON, errors.New("Access denied: cert access denied"))
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if c.Get("box.tail1234.ts.net") == nil {
		t.Fatal("failed refresh dropped the certificate")
	}
}
