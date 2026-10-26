package wiredx

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestForwardPipesBothWaysWithHalfClose(t *testing.T) {
	// Upstream echoes everything it reads, then reports EOF by closing.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() {
				b, _ := io.ReadAll(c)
				_, _ = c.Write([]byte(strings.ToUpper(string(b))))
				_ = c.Close()
			}()
		}
	}()
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Forward(ctx, front, func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", up.Addr().String())
		}, nil)
	}()
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", front.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("hello relay"))
		_ = c.(*net.TCPConn).CloseWrite() // the upstream only answers after EOF
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, err := bufio.NewReader(c).ReadString(0)
		if err != io.EOF || got != "HELLO RELAY" {
			t.Fatalf("got %q %v", got, err)
		}
		c.Close()
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forward did not stop")
	}
}

func TestStubUnavailable(t *testing.T) {
	if Available() {
		t.Skip("built with wired")
	}
	if _, err := Join(JoinOptions{}); err != ErrUnavailable {
		t.Fatalf("stub join: %v", err)
	}
}
