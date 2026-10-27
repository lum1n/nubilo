//go:build wired

package wiredx

import (
	"context"
	"net"

	wired "github.com/lum1n/wired/bindings/go"
)

// Available reports whether this binary includes wired support.
func Available() bool { return true }

type node struct{ n *wired.Node }

func Join(o JoinOptions) (Node, error) {
	n, err := wired.Join(wired.JoinOptions{Name: o.Name, Invite: o.Invite, StateDir: o.StateDir})
	if err != nil {
		return nil, err
	}
	return node{n}, nil
}

func Anchor(o AnchorOptions) (Node, error) {
	n, err := wired.Anchor(wired.AnchorOptions{
		Name: o.Name, PublicHost: o.PublicHost, Port: o.Port, StateDir: o.StateDir,
		RelayURL: o.RelayURL, RelayBind: o.RelayBind, NoQAD: o.NoQAD,
	})
	if err != nil {
		return nil, err
	}
	return node{n}, nil
}

func (x node) Name() string { return x.n.Name() }

func (x node) Listen(service string) (net.Listener, error) { return x.n.Listen(service) }

func (x node) Dial(ctx context.Context, target string) (net.Conn, error) {
	c, err := x.n.Dial(ctx, target)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (x node) Invite() string { return x.n.Invite() }

func (x node) Members() []Member {
	var out []Member
	for _, m := range x.n.Members() {
		out = append(out, Member{Name: m.Name, ID: m.ID, Anchor: m.Anchor})
	}
	return out
}

func (x node) Revoke(name string) (string, error) { return x.n.Revoke(name) }

func (x node) RotateInvite() (string, error) { return x.n.RotateInvite() }

func (x node) Path(name string) string { return x.n.Path(name).String() }

func (x node) Close() error { return x.n.Close() }
