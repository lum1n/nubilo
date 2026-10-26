package dav

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"nubilo/internal/identity"
)

type failGate struct {
	mu        sync.Mutex
	fail      map[string][]time.Time
	lockUntil map[string]time.Time
	limit     int
}

func newFailGate() *failGate {
	return &failGate{fail: map[string][]time.Time{}, lockUntil: map[string]time.Time{}, limit: 5}
}

// newIPFailGate tolerates more failures: several family members may sit
// behind one NAT address.
func newIPFailGate() *failGate {
	g := newFailGate()
	g.limit = 20
	return g
}

func (g *failGate) locked(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.lockUntil[id]; ok && time.Now().Before(until) {
		return true
	}
	cut := time.Now().Add(-time.Minute)
	arr := g.fail[id]
	kept := arr[:0]
	for _, t := range arr {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	g.fail[id] = kept
	return len(kept) >= g.limit
}

func (g *failGate) add(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fail[id] = append(g.fail[id], time.Now())
	if len(g.fail[id]) >= g.limit {
		g.lockUntil[id] = time.Now().Add(30 * time.Second)
	}
}

func (g *failGate) clear(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fail, id)
	delete(g.lockUntil, id)
}

type Auth struct {
	IDs    *identity.Service
	gate   *failGate
	ipGate *failGate
}

func NewAuth(ids *identity.Service) *Auth {
	return &Auth{IDs: ids, gate: newFailGate(), ipGate: newIPFailGate()}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user == "" || pass == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="nubilo"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Failures lock both the username and the client IP, so one IP cannot
		// spray many usernames and one username cannot be tried from many IPs.
		ipKey := "ip:" + clientIP(r)
		userKey := "user:" + strings.ToLower(user)
		if dev, ok := a.IDs.CachedBasic(r.Context(), user, pass); ok && !a.ipGate.locked(ipKey) {
			next.ServeHTTP(w, r.WithContext(WithDevice(r.Context(), dev)))
			return
		}
		if a.gate.locked(userKey) || a.ipGate.locked(ipKey) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
		dev, err := a.IDs.AuthenticateBasic(r.Context(), user, pass)
		if err != nil {
			a.gate.add(userKey)
			a.ipGate.add(ipKey)
			w.Header().Set("WWW-Authenticate", `Basic realm="nubilo"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.gate.clear(userKey)
		next.ServeHTTP(w, r.WithContext(WithDevice(r.Context(), dev)))
	})
}
