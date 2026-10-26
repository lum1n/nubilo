package wiredx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// DialFunc opens the upstream side of a forwarded connection.
type DialFunc func(ctx context.Context) (net.Conn, error)

// ForwardOptions tunes Forward.
type ForwardOptions struct {
	// SendProxyHeader writes a PROXY v1 line with the client address first.
	SendProxyHeader bool
}

// Forward accepts connections on ln and pipes each to a fresh upstream from
// dial, copying both directions with half-close so TLS close_notify and
// HTTP/1.1 request bodies behave. Bytes are passed through untouched, which
// keeps TLS (and its ALPN) end to end. It returns when ctx ends or ln fails.
func Forward(ctx context.Context, ln net.Listener, dial DialFunc, log *slog.Logger, opts ...ForwardOptions) error {
	var opt ForwardOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if log == nil {
		log = slog.Default()
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := 5 * time.Millisecond
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(backoff)
				backoff = min(2*backoff, time.Second)
				continue
			}
			return err
		}
		backoff = 5 * time.Millisecond
		wg.Add(1)
		go func() {
			defer wg.Done()
			dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			up, err := dial(dctx)
			cancel()
			if err != nil {
				log.Warn("forward_dial", "err", err.Error(), "from", c.RemoteAddr().String())
				_ = c.Close()
				return
			}
			if opt.SendProxyHeader {
				if _, err := up.Write([]byte(ProxyHeader(c.RemoteAddr(), c.LocalAddr()))); err != nil {
					_ = c.Close()
					_ = up.Close()
					return
				}
			}
			Pipe(c, up)
		}()
	}
}

type closeWriter interface {
	CloseWrite() error
}

// Pipe copies a↔b until both directions finish, then closes both.
func Pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}
