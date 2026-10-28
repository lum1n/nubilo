package wiredx

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The relay prepends a PROXY protocol v1 line to each forwarded connection
// so the home server sees the real client address (rate limits, audit). Only
// the overlay listener parses it: overlay members are the operator's own
// nodes, so the header is trusted there. The loopback Funnel listener
// (public.tailscale.funnel_listen) parses the same header from tailscaled.

// ProxyHeader formats the PROXY v1 line for a connection from src to dst.
func ProxyHeader(src, dst net.Addr) string {
	s, sok := src.(*net.TCPAddr)
	d, dok := dst.(*net.TCPAddr)
	if !sok || !dok {
		return "PROXY UNKNOWN\r\n"
	}
	fam := "TCP4"
	if s.IP.To4() == nil {
		fam = "TCP6"
	}
	return fmt.Sprintf("PROXY %s %s %s %d %d\r\n", fam, s.IP, d.IP, s.Port, d.Port)
}

// ProxyListener wraps ln so accepted conns report the client address from a
// leading PROXY v1 header. Conns without a header keep their own address.
func ProxyListener(ln net.Listener) net.Listener { return &proxyListener{Listener: ln} }

// RequireProxyListener is ProxyListener for a listener that only a proxy
// should reach (the loopback Funnel listener): conns without a PROXY header
// naming a TCP4/TCP6 client fail instead of keeping the proxy's own
// (loopback) address, which the server would otherwise treat as local.
func RequireProxyListener(ln net.Listener) net.Listener {
	return &proxyListener{Listener: ln, require: true}
}

type proxyListener struct {
	net.Listener
	require bool
}

func (l *proxyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &proxyConn{Conn: c, require: l.require}, nil
}

// proxyConn reads the header lazily on first Read or RemoteAddr so Accept
// never blocks on a slow client.
type proxyConn struct {
	net.Conn
	require bool
	once    sync.Once
	r       *bufio.Reader
	remote  net.Addr
	err     error
}

func (c *proxyConn) init() {
	c.once.Do(func() {
		c.r = bufio.NewReaderSize(c.Conn, 256)
		_ = c.Conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		defer c.Conn.SetReadDeadline(time.Time{})
		peek, err := c.r.Peek(6)
		if err != nil || string(peek) != "PROXY " {
			if c.require {
				c.err = errMissingProxy
			}
			return
		}
		line, err := c.r.ReadString('\n')
		if err != nil || len(line) > 107 {
			c.err = errors.New("wired: bad PROXY header")
			return
		}
		c.remote = parseProxyLine(strings.TrimRight(line, "\r\n"))
		if c.remote == nil && c.require {
			c.err = errMissingProxy
		}
	})
}

var errMissingProxy = errors.New("wired: PROXY header with a client address required")

func parseProxyLine(line string) net.Addr {
	f := strings.Fields(line)
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil
	}
	ip := net.ParseIP(f[2])
	port, err := strconv.Atoi(f[4])
	if ip == nil || err != nil || port < 0 || port > 65535 {
		return nil
	}
	return &net.TCPAddr{IP: ip, Port: port}
}

func (c *proxyConn) Read(b []byte) (int, error) {
	c.init()
	if c.err != nil {
		return 0, c.err
	}
	return c.r.Read(b)
}

func (c *proxyConn) RemoteAddr() net.Addr {
	c.init()
	if c.remote != nil {
		return c.remote
	}
	return c.Conn.RemoteAddr()
}
