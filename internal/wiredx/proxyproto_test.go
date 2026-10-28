package wiredx

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestProxyHeaderRoundTrip(t *testing.T) {
	home, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pl := ProxyListener(home)
	defer pl.Close()
	got := make(chan string, 1)
	go func() {
		c, err := pl.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		got <- c.RemoteAddr().String() + "|" + string(b)
		c.Close()
	}()
	front, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Forward(ctx, front, func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", home.Addr().String())
	}, nil, ForwardOptions{SendProxyHeader: true})
	c, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := c.LocalAddr().String()
	_, _ = c.Write([]byte("GET / HTTP/1.1"))
	_ = c.(*net.TCPConn).CloseWrite()
	select {
	case s := <-got:
		if s != client+"|GET / HTTP/1.1" {
			t.Fatalf("got %q want %q", s, client+"|GET / HTTP/1.1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	c.Close()
}

func TestProxyListenerWithoutHeader(t *testing.T) {
	if a := parseProxyLine("PROXY TCP4 1.2.3.4 5.6.7.8 1234 443"); a.String() != "1.2.3.4:1234" {
		t.Fatalf("parsed %v", a)
	}
	if a := parseProxyLine("PROXY UNKNOWN"); a != nil {
		t.Fatalf("unknown parsed as %v", a)
	}
	if a := parseProxyLine("PROXY TCP4 nope 5.6.7.8 1 2"); a != nil {
		t.Fatal("bad ip accepted")
	}
}

func TestRequireProxyListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pl := RequireProxyListener(ln)
	defer pl.Close()
	got := make(chan string, 3)
	go func() {
		for {
			c, err := pl.Accept()
			if err != nil {
				return
			}
			b, err := io.ReadAll(c)
			if err != nil {
				got <- "err"
			} else {
				got <- c.RemoteAddr().String() + "|" + string(b)
			}
			c.Close()
		}
	}()
	send := func(s string) string {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write([]byte(s))
		_ = c.(*net.TCPConn).CloseWrite()
		select {
		case r := <-got:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
			return ""
		}
	}
	if r := send("PROXY TCP6 2001:db8::1 ::1 4000 8444\r\nhi"); r != "[2001:db8::1]:4000|hi" {
		t.Fatalf("with header: %q", r)
	}
	if r := send("GET / HTTP/1.1\r\n"); r != "err" {
		t.Fatalf("without header: %q", r)
	}
	if r := send("PROXY UNKNOWN\r\nhi"); r != "err" {
		t.Fatalf("unknown: %q", r)
	}
}
