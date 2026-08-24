package dav

import (
	"net/http"
	"sync"
	"time"

	"nubilo/internal/identity"
)

type failGate struct {
	mu        sync.Mutex
	fail      map[string][]time.Time
	lockUntil map[string]time.Time
}

func newFailGate() *failGate {
	return &failGate{fail: map[string][]time.Time{}, lockUntil: map[string]time.Time{}}
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
	return len(kept) >= 5
}

func (g *failGate) add(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fail[id] = append(g.fail[id], time.Now())
	if len(g.fail[id]) >= 5 {
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
	IDs  *identity.Service
	gate *failGate
}

func NewAuth(ids *identity.Service) *Auth {
	return &Auth{IDs: ids, gate: newFailGate()}
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user == "" || pass == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="nubilo"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if a.gate.locked(user) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
		dev, err := a.IDs.AuthenticatePassword(r.Context(), user, pass)
		if err != nil {
			a.gate.add(user)
			w.Header().Set("WWW-Authenticate", `Basic realm="nubilo"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.gate.clear(user)
		next.ServeHTTP(w, r.WithContext(WithDevice(r.Context(), dev)))
	})
}
