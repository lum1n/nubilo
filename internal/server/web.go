package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
	"nubilo/internal/qr"
)

// The web app at /web/ lets a user reach their photos and files from any
// browser. Signing in needs no password: the page shows a code, and the
// Nubilo app on a paired phone or Mac approves it (identity.StartWebLogin).
// The session then acts as that approving device, through the same handlers
// the apps use, so it never has more access than the app that let it in. The
// admin console (`nubilo ui`) stays loopback-only.

//go:embed webui
var webFS embed.FS

const (
	// __Host- cookies must be Secure, host-only and Path=/, so a sibling
	// subdomain cannot plant one (login CSRF by cookie tossing).
	webSessionCookie = "__Host-nubilo_web"
	webLoginCookie   = "__Host-nubilo_web_login"
	webCSP           = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"
)

func (s *Server) webRoutes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "webui")
	assets := http.StripPrefix("/web/assets/", http.FileServer(http.FS(static)))
	mux.Handle("GET /web/assets/", webHeaders(assets))
	mux.HandleFunc("GET /web", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /web/{rest...}", s.serveWebIndex)

	mux.HandleFunc("POST /web/api/login", s.webAPI(s.handleWebLoginStart))
	mux.HandleFunc("GET /web/api/login", s.webAPI(s.handleWebLoginPoll))
	mux.HandleFunc("POST /web/api/logout", s.webAPI(s.handleWebLogout))

	// Session routes reuse the app API handlers; the session's device is put
	// in the request context exactly as signature auth would.
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /web/api/me":                   s.handleMe,
		"GET /web/api/collections":          s.handleCollectionList,
		"POST /web/api/objects":             s.handleObjects,
		"POST /web/api/object":              s.handleObject,
		"POST /web/api/collection":          s.handleEnsureCollection,
		"POST /web/api/collection/update":   s.handleCollectionUpdate,
		"POST /web/api/collection/delete":   s.handleCollectionDelete,
		"POST /web/api/push":                s.handlePush,
		"PUT /web/api/blob/{hash}":          s.handleBlobPut,
		"GET /web/api/files/{id}":           s.handleWebDownload,
		"GET /web/api/photos":               s.handlePhotosList,
		"POST /web/api/photos":              s.handlePhotosUpload,
		"GET /web/api/photos/{id}":          s.handlePhotoMeta,
		"GET /web/api/photos/{id}/original": s.handlePhotoOriginal,
		"GET /web/api/photos/{id}/preview":  s.handlePhotoPreview,
		"GET /web/api/photos/{id}/thumb":    s.handlePhotoThumb,
		"GET /web/api/albums":               s.handleAlbums,
		"GET /web/api/albums/{id}/items":    s.handleAlbumItems,
	} {
		mux.HandleFunc(pattern, s.webAPI(s.webSession(h)))
	}

	// The app side: approve a sign-in and manage sessions.
	mux.HandleFunc("GET /api/v1/me/web-login", s.authed(s.handleWebLoginInfo))
	mux.HandleFunc("POST /api/v1/me/web-login", s.authed(s.handleWebLoginApprove))
	mux.HandleFunc("GET /api/v1/me/web-sessions", s.authed(s.handleWebSessions))
	mux.HandleFunc("DELETE /api/v1/me/web-sessions/{id}", s.authed(s.handleWebSessionEnd))
}

func webHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", webCSP)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveWebIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/web/api/") {
		http.NotFound(w, r)
		return
	}
	body, err := webFS.ReadFile("webui/index.html")
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	webHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})).ServeHTTP(w, r)
}

// webAPI adds the security headers and, for anything that changes state,
// requires the X-Nubilo-Web header and a same-origin Origin. Together with
// SameSite=Strict cookies this stops cross-site requests.
func (s *Server) webAPI(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Nubilo-Web") != "1" || !sameOrigin(r, s.Cfg.PublicURL()) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		webHeaders(h).ServeHTTP(w, r)
	}
}

// sameOrigin reports whether a state-changing request comes from this
// server's own pages: Origin names the host the browser used (r.Host, which
// the relay edge preserves) or the configured public URL.
func sameOrigin(r *http.Request, publicURL string) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		// Browsers send Origin on every cross-origin and on same-origin
		// non-GET fetches; its absence means a non-browser client.
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	p, err := url.Parse(publicURL)
	return publicURL != "" && err == nil && strings.EqualFold(u.Host, p.Host)
}

func (s *Server) webSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(webSessionCookie)
		if err != nil {
			http.Error(w, "signed out", http.StatusUnauthorized)
			return
		}
		sess, dev, err := s.IDs.AuthenticateWebSession(r.Context(), c.Value)
		if err != nil {
			clearCookie(w, webSessionCookie, "/")
			http.Error(w, "signed out", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), deviceKey, dev)
		ctx = context.WithValue(ctx, webSessionKey, sess)
		h(w, r.WithContext(ctx))
	}
}

const webSessionKey ctxKey = 2

func setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) handleWebLoginStart(w http.ResponseWriter, r *http.Request) {
	if !s.webLoginLim.Allow(clientIP(r)) {
		http.Error(w, "too many sign-in attempts; try again later", http.StatusTooManyRequests)
		return
	}
	l, secret, err := s.IDs.StartWebLogin(r.Context(), r.UserAgent(), clientIP(r))
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	setCookie(w, webLoginCookie, secret, "/", identity.WebLoginTTL)
	link := "nubilo://web-login?" + url.Values{"url": {s.baseURL(r)}, "code": {l.Code}}.Encode()
	png, _ := qr.PNGDataURL(link, 256)
	s.writeJSON(w, http.StatusCreated, map[string]any{"code": l.Code, "expires_at": l.ExpiresAt, "link": link, "qr_png": png})
}

func (s *Server) handleWebLoginPoll(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(webLoginCookie)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
		return
	}
	token, sess, err := s.IDs.PollWebLogin(r.Context(), c.Value)
	switch {
	case err != nil:
		clearCookie(w, webLoginCookie, "/")
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
	case token == "":
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	default:
		clearCookie(w, webLoginCookie, "/")
		setCookie(w, webSessionCookie, token, "/", identity.WebSessionMaxLife)
		s.Audit.Event(r.Context(), sess.DeviceID, "web.signin", map[string]any{"session": sess.ID, "ip": sess.IP})
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "approved"})
	}
}

func (s *Server) handleWebLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(webSessionCookie); err == nil {
		if sess, _, err := s.IDs.AuthenticateWebSession(r.Context(), c.Value); err == nil {
			_ = s.IDs.EndWebSession(r.Context(), "", sess.ID)
		}
	}
	clearCookie(w, webSessionCookie, "/")
	w.WriteHeader(http.StatusNoContent)
}

// handleWebDownload sends a file object's bytes with its name, for links in
// the web app.
func (s *Server) handleWebDownload(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	obj, err := s.Engine.GetObject(r.Context(), r.PathValue("id"))
	if err != nil || obj.DeletedAt != nil || obj.BlobID == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if access, err := s.Engine.AccessFor(r.Context(), dev, obj.CollectionID); err != nil || access < authz.AccessRead {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, err := s.Store.GetBlobPlaintext(obj.BlobID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var meta struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(obj.Metadata, &meta)
	name := path.Base(strings.ReplaceAll(meta.Name, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = obj.ID
	}
	// Never let a stored file render as a page on this origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("ETag", `"`+obj.ContentHash+`"`)
	http.ServeContent(w, r, "", time.UnixMilli(obj.UpdatedAt), bytes.NewReader(body))
}

// --- app side ---

func (s *Server) handleWebLoginInfo(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	if !s.webApproveLim.Allow(dev.ID) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	l, err := s.IDs.PendingWebLogin(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "unknown or expired code", http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleWebLoginApprove(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if dev.Role != identity.RoleClient {
		http.Error(w, "only the Nubilo app can approve web sign-in", http.StatusForbidden)
		return
	}
	if !s.webApproveLim.Allow(dev.ID) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	if err := s.IDs.ApproveWebLogin(r.Context(), dev, req.Code); err != nil {
		if errors.Is(err, identity.ErrWebLogin) {
			s.Audit.Event(r.Context(), dev.ID, "web.approve_fail", nil)
			http.Error(w, "unknown or expired code", http.StatusNotFound)
			return
		}
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "web.approve", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebSessions(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	list, err := s.IDs.ListWebSessions(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) handleWebSessionEnd(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	if err := s.IDs.EndWebSession(r.Context(), dev.UserID, r.PathValue("id")); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "web.signout", map[string]any{"session": r.PathValue("id")})
	w.WriteHeader(http.StatusNoContent)
}
