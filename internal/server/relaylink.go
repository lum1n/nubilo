package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"nubilo/internal/update"
	"nubilo/internal/wiredx"
)

// RelayRemote is the relay's updater, reached over this server's overlay
// node (the relay's admin service; see relay.ServeAdmin).
func (s *Server) RelayRemote() update.Remote { return relayRemote{s} }

type relayRemote struct{ s *Server }

func (r relayRemote) client() (*http.Client, error) {
	r.s.mu.Lock()
	node := r.s.node
	r.s.mu.Unlock()
	if node == nil {
		return nil, errors.New("not connected to the relay")
	}
	anchor := ""
	for _, m := range node.Members() {
		if m.Anchor {
			anchor = m.Name
			break
		}
	}
	if anchor == "" {
		return nil, errors.New("the overlay has no relay")
	}
	target := wiredx.ServiceAdmin + "@" + anchor
	return &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return node.Dial(ctx, target) },
		DisableKeepAlives: true,
	}}, nil
}

func (r relayRemote) Status(ctx context.Context) (update.Status, string, error) {
	var out update.RemoteStatus
	if err := r.call(ctx, http.MethodGet, "/update", nil, &out); err != nil {
		return update.Status{}, "", err
	}
	return out.Status, out.WiredProtocol, nil
}

func (r relayRemote) Install(ctx context.Context, version string) error {
	body, _ := json.Marshal(map[string]string{"version": version})
	return r.call(ctx, http.MethodPost, "/update/install", body, nil)
}

func (r relayRemote) call(ctx context.Context, method, path string, body []byte, out any) error {
	c, err := r.client()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://relay"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		// An older relay without the admin service refuses the stream.
		return fmt.Errorf("the relay did not answer (it may be too old to update itself): %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return fmt.Errorf("relay: %s", strings.TrimSpace(firstNonEmpty(e.Error, string(b))))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
