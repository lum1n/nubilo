// Package wiredx connects Nubilo to a wired overlay network
// (github.com/lum1n/wired): the home server joins as a member and serves
// HTTPS over it, and a small VPS runs the anchor + relay that forwards public
// TCP 443/80 into the overlay. Builds without the `wired` tag get a stub that
// reports ErrUnavailable, so the default build needs no Rust toolchain.
package wiredx

import (
	"context"
	"errors"
	"net"
)

// ErrUnavailable means this binary was built without wired support.
var ErrUnavailable = errors.New("wired: not built into this binary (rebuild with -tags wired)")

// Member is a node in the overlay.
type Member struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Anchor bool   `json:"anchor"`
}

// Node is a joined or anchoring overlay node.
type Node interface {
	// Name is this node's name in the network.
	Name() string
	// Listen accepts streams for service from any member.
	Listen(service string) (net.Listener, error)
	// Dial opens a stream to target ("service@node").
	Dial(ctx context.Context, target string) (net.Conn, error)
	// Invite is the network invite (anchors only; a credential).
	Invite() string
	Members() []Member
	// Revoke removes a node and rotates the invite (root anchor only).
	Revoke(name string) (newInvite string, err error)
	RotateInvite() (string, error)
	// Path reports how traffic to node flows: "direct", "relay" or "none".
	Path(node string) string
	Close() error
}

// JoinOptions configures a member node (the home server).
type JoinOptions struct {
	Name     string
	Invite   string
	StateDir string
}

// AnchorOptions configures the anchor + relay (the VPS).
type AnchorOptions struct {
	Name string
	// PublicHost is the host or IP put into the invite.
	PublicHost string
	// Port is the UDP peer port and TCP relay port (default 7443).
	Port     uint16
	StateDir string
	// RelayURL and RelayBind serve the wired relay behind a TLS-terminating
	// proxy (exe.dev) instead of on public ports: peers reach it at RelayURL,
	// and the proxy hands /relay and /ping to the plain-HTTP RelayBind.
	RelayURL, RelayBind string
	// NoQAD turns off UDP address discovery (nothing is reachable behind a proxy).
	NoQAD bool
}

// Ports the anchor needs open on the VPS firewall.
const (
	AnchorPort = 7443 // UDP peers + TCP relay
	QADPort    = 7842 // UDP address discovery
)

// Service names the home server listens on inside the overlay.
const (
	ServiceHTTPS = "https"
	ServiceHTTP  = "http"
	// ServiceWeb is plain HTTP for a relay behind a TLS-terminating proxy:
	// TLS already ended at the proxy, and wired encrypts the hop home.
	ServiceWeb = "web"
)

// ClientIPHeader carries the client address from a relay behind a proxy to
// the home server's web service. The relay always overwrites it, and only
// the overlay web listener reads it.
const ClientIPHeader = "X-Nubilo-Client-IP"
