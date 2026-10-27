package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nubilo/internal/auth"
	"nubilo/internal/authz"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/profile"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

const profileTokenTTL = 15 * time.Minute

// authedUpload accepts a signed request or an upload bearer token
// (POST /api/v1/me/upload-token). Only blob upload and photo commit use it.
func (s *Server) authedUpload(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			dev, err := s.IDs.AuthenticateUploadToken(r.Context(), strings.TrimSpace(tok))
			if err != nil {
				s.rejectAuth(w, r, auth.ErrSignature)
				return
			}
			h(w, r.WithContext(context.WithValue(r.Context(), deviceKey, dev)))
			return
		}
		s.authed(h)(w, r)
	}
}

// userDevice rejects system devices and devices without a user.
func userDevice(w http.ResponseWriter, r *http.Request) *identity.Device {
	dev := deviceFrom(r)
	if dev == nil || dev.UserID == "" || dev.Revoked() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	return dev
}

// baseURL is the public URL (domain or Tailscale name) when configured,
// else the URL the client used to reach us.
func (s *Server) baseURL(r *http.Request) string {
	if u := s.Cfg.PublicURL(); u != "" {
		return u
	}
	return "https://" + r.Host
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	u, err := s.IDs.GetUser(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	usage, err := s.Engine.UserUsage(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	base := s.baseURL(r)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"user":    u,
		"device":  toDeviceRow(dev),
		"storage": usage[u.ID],
		"urls": map[string]string{
			"caldav":  base + profile.PrincipalPath("/caldav", u.Username),
			"carddav": base + profile.PrincipalPath("/carddav", u.Username),
			"webdav":  base + "/dav/",
			"photos":  base + "/api/v1/photos",
		},
		// Apps paired on a LAN address move to this once it answers for them.
		"public_url": s.Cfg.PublicURL(),
	})
}

func (s *Server) handleMyPasswords(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	list, err := s.IDs.ListAppPasswords(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []identity.AppPassword{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"passwords": list})
}

func (s *Server) handleCreatePassword(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	if dev.Role == identity.RoleDAV {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Label string `json:"label"`
		Scope string `json:"scope"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, err := s.IDs.GetUser(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	pw, plain, err := s.IDs.CreateAppPassword(r.Context(), dev.UserID, req.Label, req.Scope)
	if errors.Is(err, identity.ErrTooManyPasswords) {
		http.Error(w, "too many app passwords", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "password.create", map[string]any{"id": pw.ID})
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": pw.ID, "username": u.Username, "password": plain})
}

func (s *Server) handleRevokePassword(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	id := r.PathValue("id")
	if err := s.IDs.RevokeAppPassword(r.Context(), dev.UserID, id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "password.revoke", map[string]any{"id": id})
	s.writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

// handleCreateProfile makes a fresh CalDAV/CardDAV app password, wraps it in
// a .mobileconfig, and returns a single-use URL for Safari to download it.
func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	if dev.Role == identity.RoleDAV {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Services []string `json:"services"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	acct := profile.Account{ServerName: s.Cfg.Public.Name(), Identifier: b64(s.ServerPub)}
	for _, svc := range req.Services {
		switch svc {
		case "caldav":
			acct.CalDAV = true
		case "carddav":
			acct.CardDAV = true
		default:
			http.Error(w, "unknown service", http.StatusBadRequest)
			return
		}
	}
	if len(req.Services) == 0 {
		acct.CalDAV, acct.CardDAV = true, true
	}
	u, err := s.IDs.GetUser(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	acct.Username = u.Username
	acct.Host, acct.Port = s.publicHostPort(r)
	scope := []string{}
	if acct.CalDAV {
		scope = append(scope, "caldav")
	}
	if acct.CardDAV {
		scope = append(scope, "carddav")
	}
	label := "Profile " + time.Now().Format("2006-01-02") + " (" + dev.Name + ")"
	if len(label) > 128 {
		label = label[:128]
	}
	pw, plain, err := s.IDs.CreateAppPassword(r.Context(), dev.UserID, label, strings.Join(scope, ","))
	if errors.Is(err, identity.ErrTooManyPasswords) {
		http.Error(w, "too many app passwords; revoke one first", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	acct.Password = plain
	body, err := profile.Build(acct)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// The payload holds a plaintext password; keep it encrypted at rest and
	// only until first download or expiry.
	enc, err := ncrypto.EncryptBlob(s.Store.BlobKey, body)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	tok, err := ncrypto.Random(24)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	id := b64url(tok)
	exp := time.Now().Add(profileTokenTTL).UnixMilli()
	if _, err := s.Store.DB.ExecContext(r.Context(), `
		INSERT INTO profile_tokens(id, user_id, payload, expires_at) VALUES (?, ?, ?, ?)
	`, id, dev.UserID, enc, exp); err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	_, _ = s.Store.DB.ExecContext(r.Context(), `DELETE FROM profile_tokens WHERE expires_at < ? OR used_at IS NOT NULL`, store.NowMS())
	s.Audit.Event(r.Context(), dev.ID, "profile.create", map[string]any{"password_id": pw.ID})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"url":         s.baseURL(r) + "/api/v1/profile/" + id,
		"expires_at":  exp,
		"password_id": pw.ID,
	})
}

func (s *Server) publicHostPort(r *http.Request) (string, int) {
	if host, port, ok := s.Cfg.PublicHostPort(); ok {
		return host, port
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		return r.Host, 443
	}
	p, _ := strconv.Atoi(port)
	return host, p
}

func (s *Server) handleProfileDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("token")
	if len(id) < 16 || len(id) > 64 {
		http.NotFound(w, r)
		return
	}
	var enc []byte
	err := s.Store.WithWrite(r.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRow(`
			SELECT payload FROM profile_tokens WHERE id = ? AND used_at IS NULL AND expires_at > ?
		`, id, store.NowMS()).Scan(&enc); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE profile_tokens SET used_at = ?, payload = x'' WHERE id = ?`, store.NowMS(), id)
		return err
	})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body, err := ncrypto.DecryptBlob(s.Store.BlobKey, enc)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", profile.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="nubilo.mobileconfig"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) handleUploadToken(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil || dev.Role == identity.RoleDAV {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	tok, exp, err := s.IDs.CreateUploadToken(r.Context(), dev.ID, 24*time.Hour)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": exp})
}

// handleDeviceInvite lets a user add another device to their own account:
// a single-use invite bound to them that expires in an hour. App passwords
// (dav) cannot mint one, so a leaked CalDAV password does not become a
// full device.
func (s *Server) handleDeviceInvite(w http.ResponseWriter, r *http.Request) {
	dev := userDevice(w, r)
	if dev == nil {
		return
	}
	if dev.Role == identity.RoleDAV {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	code, inv, err := s.IDs.CreateInvite(r.Context(), dev.UserID, "", dev.UserID, time.Hour, 1)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "invite.create", map[string]any{"invite": inv.ID, "role": inv.Role, "for_user": inv.UserID, "self": true})
	s.writeInvite(w, r, code, inv)
}

func (s *Server) handleUserDirectory(w http.ResponseWriter, r *http.Request) {
	if dev := deviceFrom(r); dev == nil || dev.Revoked() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	users, err := s.IDs.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	type entry struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}
	out := []entry{}
	for _, u := range users {
		if u.Active() {
			out = append(out, entry{u.ID, u.Username, u.DisplayName})
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) handleCollectionList(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if dev == nil || dev.Revoked() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cols, err := s.Engine.GetCollections(r.Context(), syncengine.ScopeOf(dev))
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	type row struct {
		syncengine.Collection
		Shares []syncengine.Share `json:"shares,omitempty"`
	}
	out := []row{}
	for _, c := range cols {
		if c.DeletedAt != nil {
			continue
		}
		rw := row{Collection: c}
		if c.Access == authz.AccessOwner.String() && c.ParentID == "" {
			rw.Shares, _ = s.Engine.ListShares(r.Context(), c.ID)
		}
		out = append(out, rw)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}

// ownedRoot loads a root collection the caller owns, or writes an error.
func (s *Server) ownedRoot(w http.ResponseWriter, r *http.Request) (*identity.Device, *syncengine.Collection) {
	dev := deviceFrom(r)
	id := r.PathValue("id")
	if dev == nil || !ids.Valid(id) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, nil
	}
	access, err := s.Engine.AccessFor(r.Context(), dev, id)
	if err != nil || access == authz.AccessNone {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, nil
	}
	if err := authz.AllowCollection(dev, authz.CollectionAdmin, access); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil
	}
	c, err := s.Engine.GetCollection(r.Context(), id)
	if err != nil || c.DeletedAt != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, nil
	}
	if c.ParentID != "" {
		http.Error(w, "only top-level collections can be shared", http.StatusBadRequest)
		return nil, nil
	}
	return dev, c
}

func (s *Server) handleSharesGet(w http.ResponseWriter, r *http.Request) {
	_, c := s.ownedRoot(w, r)
	if c == nil {
		return
	}
	shares, err := s.Engine.ListShares(r.Context(), c.ID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

type shareReq struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Username      string `json:"username"`
	Access        string `json:"access"`
}

// principal resolves a share request to a user id (by id or username).
func (s *Server) principal(ctx context.Context, req shareReq) (string, error) {
	if req.PrincipalType == "" {
		req.PrincipalType = "user"
	}
	if req.PrincipalType != "user" {
		return "", fmt.Errorf("unsupported principal type")
	}
	if req.PrincipalID == "" && req.Username != "" {
		u, err := s.IDs.GetUserByName(ctx, req.Username)
		if err != nil {
			return "", err
		}
		return u.ID, nil
	}
	if _, err := s.IDs.GetUser(ctx, req.PrincipalID); err != nil {
		return "", err
	}
	return req.PrincipalID, nil
}

func (s *Server) handleSharesPut(w http.ResponseWriter, r *http.Request) {
	dev, c := s.ownedRoot(w, r)
	if c == nil {
		return
	}
	var req shareReq
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	uid, err := s.principal(r.Context(), req)
	if err != nil {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	by := dev.UserID
	if by == "" {
		by = dev.ID
	}
	if err := s.Engine.SetShare(r.Context(), c.ID, "user", uid, req.Access, by); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "share.set", map[string]any{"collection": c.ID, "principal": uid, "access": req.Access})
	s.handleSharesGet(w, r)
}

func (s *Server) handleSharesDelete(w http.ResponseWriter, r *http.Request) {
	dev, c := s.ownedRoot(w, r)
	if c == nil {
		return
	}
	req := shareReq{PrincipalType: r.URL.Query().Get("principal_type"), PrincipalID: r.URL.Query().Get("principal_id"), Username: r.URL.Query().Get("username")}
	if req.PrincipalID == "" && req.Username == "" {
		if err := s.readJSON(r, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	uid, err := s.principal(r.Context(), req)
	if err != nil {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	if err := s.Engine.RemoveShare(r.Context(), c.ID, "user", uid); err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.Audit.Event(r.Context(), dev.ID, "share.remove", map[string]any{"collection": c.ID, "principal": uid})
	s.handleSharesGet(w, r)
}

func (s *Server) handleInviteRedeem(w http.ResponseWriter, r *http.Request) {
	if !s.redeemLim.Allow(clientIP(r)) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Code        string `json:"code"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		DeviceName  string `json:"device_name"`
		PublicKey   string `json:"public_key"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pub, err := decodeKey(req.PublicKey)
	if err != nil {
		http.Error(w, "bad public key", http.StatusBadRequest)
		return
	}
	res, err := s.IDs.RedeemInvite(r.Context(), req.Code, identity.RedeemRequest{
		Username: req.Username, DisplayName: req.DisplayName, DeviceName: req.DeviceName, PublicKey: pub,
	})
	if err != nil {
		s.Audit.Event(r.Context(), "", "invite.redeem_fail", map[string]any{"class": err.Error()})
		switch {
		case errors.Is(err, identity.ErrUserExists):
			http.Error(w, "username taken", http.StatusConflict)
		case errors.Is(err, identity.ErrUsername):
			http.Error(w, "invalid username", http.StatusBadRequest)
		case errors.Is(err, identity.ErrName):
			http.Error(w, "invalid name", http.StatusBadRequest)
		default:
			http.Error(w, "invalid invite", http.StatusUnauthorized)
		}
		return
	}
	s.Audit.Event(r.Context(), "", "invite.redeem", map[string]any{"user": res.User.ID, "new_user": res.NewUser})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"pairing_id": res.PairingID,
		"challenge":  b64(res.Challenge),
		"server_id":  b64(s.ServerPub),
		"user":       res.User,
	})
}

// handleJoinPage is the landing page behind invite QR codes
// (https://<domain>/join#<code>). The code stays in the fragment, so it never
// reaches the server; the page hands it to the app.
func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	name := htmlEscape(s.Cfg.Public.Name())
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Join %[1]s</title><style>body{font:17px -apple-system,system-ui,sans-serif;max-width:32em;margin:3em auto;padding:0 1em}a.b{display:inline-block;padding:.8em 1.4em;border-radius:.6em;background:#0a84ff;color:#fff;text-decoration:none}</style></head>
<body><h1>Join %[1]s</h1><p>Open this invite in the Nubilo app to set up this device.</p>
<p><a class="b" id="open" href="#">Open in Nubilo</a></p><p id="code" style="font-family:ui-monospace,monospace"></p>
<script>var c=location.hash.slice(1);var u="nubilo://setup?url="+encodeURIComponent(location.origin)+"&invite="+encodeURIComponent(c);
document.getElementById("open").href=u;document.getElementById("code").textContent=c?"Invite code: "+c:"This link has no invite code.";</script></body></html>`, name)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
