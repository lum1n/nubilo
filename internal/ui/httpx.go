package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
)

const sessionCookie = "nubilo_ui"

type sessionGate struct {
	session []byte
}

func newSessionGate() (sessionGate, error) {
	sess := make([]byte, 32)
	if _, err := rand.Read(sess); err != nil {
		return sessionGate{}, err
	}
	return sessionGate{session: sess}, nil
}

func validateLoopbackListen(listen string) (string, error) {
	if listen == "" {
		return "", errors.New("ui: listen address required")
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if !isLoopbackHost(host) {
		return "", errors.New("ui: listen address must be loopback")
	}
	return listen, nil
}

const listenPortTries = 20

// listenLoopback binds addr. If that port is in use, it tries the next ports
// on the same host. Port 0 is left to the OS.
func listenLoopback(addr string) (net.Listener, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	if port == 0 {
		return net.Listen("tcp", addr)
	}
	var last error
	for i := 0; i < listenPortTries; i++ {
		p := port + i
		if p > 65535 {
			break
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		last = err
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	if last == nil {
		return nil, fmt.Errorf("ui: no free port at or above %s", addr)
	}
	return nil, last
}

func (g sessionGate) sessionHex() string {
	return hex.EncodeToString(g.session)
}

func (g sessionGate) SessionURL(listen string) string {
	return "http://" + listen + "/?session=" + g.sessionHex()
}

func (g sessionGate) matchSession(tok string) bool {
	b, err := hex.DecodeString(strings.TrimSpace(tok))
	if err != nil || len(b) != len(g.session) {
		return false
	}
	return subtle.ConstantTimeCompare(b, g.session) == 1
}

func (g sessionGate) setSessionCookie(w http.ResponseWriter, tok string) bool {
	if !g.matchSession(tok) {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    g.sessionHex(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7,
	})
	return true
}

func (g sessionGate) sessionOK(r *http.Request) bool {
	if c, err := r.Cookie(sessionCookie); err == nil && g.matchSession(c.Value) {
		return true
	}
	if tok := strings.TrimSpace(r.Header.Get("X-Nubilo-UI")); tok != "" && g.matchSession(tok) {
		return true
	}
	return false
}

func (g sessionGate) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.sessionOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == io.EOF {
		return nil
	}
	return err
}
