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
