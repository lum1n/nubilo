package dav

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/emersion/go-webdav"

	"nubilo/internal/authz"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/syncengine"
)

// CalDAV and CardDAV homes live under a per-user principal:
//
//	/caldav/u/<username>/calendars/<seg>/<uid>.ics
//	/carddav/u/<username>/addressbooks/<seg>/<uid>.vcf
//
// <seg> is the collection ID (or the path segment a client chose with
// MKCALENDAR, kept as metadata "slug"). Accounts created before multi-user
// support use the legacy single-user layout /caldav/user/calendars/<name>/,
// which keeps working for the authenticated user.
const (
	userSeg       = "u"
	legacyUserSeg = "user"
)

type styleKey struct{}

// withLegacyStyle records that the request used the legacy /<prefix>/user/ layout.
func withLegacyStyle(ctx context.Context, legacy bool) context.Context {
	return context.WithValue(ctx, styleKey{}, legacy)
}

func legacyStyle(ctx context.Context) bool {
	v, _ := ctx.Value(styleKey{}).(bool)
	return v
}

// isLegacyPath reports whether p is under prefix/user.
func isLegacyPath(prefix, p string) bool {
	return HasPrefix(p, prefix+"/"+legacyUserSeg)
}

// handlerPrefix is the go-webdav prefix for p: go-webdav infers the resource
// type from the number of segments after it (principal, home, collection, object).
func handlerPrefix(prefix, p string) string {
	if HasPrefix(p, prefix+"/"+userSeg) {
		return prefix + "/" + userSeg
	}
	return prefix
}

// davUser is the authenticated user of a DAV request.
type davUser struct {
	dev      *identity.Device
	id       string
	username string
}

func currentUser(ctx context.Context, eng *syncengine.Engine) (*davUser, error) {
	dev := DeviceFrom(ctx)
	if dev == nil || dev.UserID == "" {
		return nil, webdav.NewHTTPError(http.StatusForbidden, errors.New("no user"))
	}
	name, err := eng.Username(ctx, dev.UserID)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return &davUser{dev: dev, id: dev.UserID, username: name}, nil
}

// homeBase returns /<prefix>/u/<username> (or the legacy /<prefix>/user).
func homeBase(ctx context.Context, prefix string, u *davUser) string {
	if legacyStyle(ctx) {
		return prefix + "/" + legacyUserSeg
	}
	return prefix + "/" + userSeg + "/" + u.username
}

// parseHomePath splits p into collection segment and file name below
// <prefix>/{u/<username>|user}/<homeSeg>/. It rejects other users' homes.
func parseHomePath(prefix, homeSeg, p string, u *davUser) (colSeg, file string, err error) {
	n, err := Normalize(p)
	if err != nil {
		return "", "", webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if !HasPrefix(n, prefix) {
		return "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	segs, err := Split(strings.TrimPrefix(n, prefix))
	if err != nil {
		return "", "", webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	switch {
	case len(segs) >= 1 && segs[0] == legacyUserSeg:
		segs = segs[1:]
	case len(segs) >= 2 && segs[0] == userSeg:
		if u == nil || !strings.EqualFold(segs[1], u.username) {
			return "", "", webdav.NewHTTPError(http.StatusForbidden, errors.New("not your home"))
		}
		segs = segs[2:]
	default:
		return "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	if len(segs) < 2 || segs[0] != homeSeg {
		return "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	if !ValidDisplayName(segs[1]) {
		return "", "", webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid collection name"))
	}
	switch len(segs) {
	case 2:
		return segs[1], "", nil
	case 3:
		if !ValidDisplayName(segs[2]) {
			return "", "", webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid object name"))
		}
		return segs[1], segs[2], nil
	}
	return "", "", webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
}

// homeCol is a collection as seen from one user's DAV home.
type homeCol struct {
	col    syncengine.Collection
	seg    string
	access authz.Access
}

type colSlug struct {
	Slug string `json:"slug,omitempty"`
}

// collectionSeg is the path segment of c in DAV homes.
func collectionSeg(c *syncengine.Collection) string {
	var m colSlug
	if len(c.Metadata) > 0 {
		_ = json.Unmarshal(c.Metadata, &m)
	}
	if m.Slug != "" && ValidDisplayName(m.Slug) {
		return m.Slug
	}
	return c.ID
}

// displayName is the collection name shown to u; shared collections carry
// the owner's username so two "Family" calendars stay distinguishable.
func (h *homeCol) displayName() string {
	if h.access == authz.AccessOwner || h.col.OwnerName == "" {
		return h.col.Name
	}
	return h.col.Name + " (" + h.col.OwnerName + ")"
}

// listHome returns u's own root collections of kind followed by roots
// shared with u.
func listHome(ctx context.Context, eng *syncengine.Engine, u *davUser, kind string) ([]homeCol, error) {
	own, err := eng.ChildCollections(ctx, u.id, kind, "")
	if err != nil {
		return nil, err
	}
	shared, err := eng.SharedRoots(ctx, u.id, kind)
	if err != nil {
		return nil, err
	}
	out := make([]homeCol, 0, len(own)+len(shared))
	for i := range own {
		out = append(out, homeCol{col: own[i], seg: collectionSeg(&own[i]), access: authz.AccessOwner})
	}
	for i := range shared {
		acc := authz.AccessRead
		if shared[i].Access == authz.AccessWrite.String() {
			acc = authz.AccessWrite
		}
		out = append(out, homeCol{col: shared[i], seg: collectionSeg(&shared[i]), access: acc})
	}
	return out, nil
}

// findHome resolves a home path segment: collection ID, then client slug,
// then (legacy) the name of one of u's own collections.
func findHome(ctx context.Context, eng *syncengine.Engine, u *davUser, kind, seg string) (*homeCol, error) {
	if ids.Valid(seg) {
		if c, err := eng.GetCollection(ctx, seg); err == nil && c.Kind == kind && c.DeletedAt == nil && c.ParentID == "" {
			acc, err := eng.AccessFor(ctx, u.dev, c.ID)
			if err != nil {
				return nil, err
			}
			if acc > authz.AccessNone {
				return &homeCol{col: *c, seg: collectionSeg(c), access: acc}, nil
			}
		}
	}
	all, err := listHome(ctx, eng, u, kind)
	if err != nil {
		return nil, err
	}
	// The caller's own collections always win, by slug and then by the
	// legacy name, so a sharer cannot shadow them with a chosen slug.
	for _, own := range []bool{true, false} {
		for i := range all {
			if (all[i].access == authz.AccessOwner) == own && all[i].seg == seg {
				return &all[i], nil
			}
		}
		if own {
			for i := range all {
				if all[i].access == authz.AccessOwner && all[i].col.Name == seg {
					return &all[i], nil
				}
			}
		}
	}
	return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
}

// need checks the protocol permission and the collection access level.
func need(ctx context.Context, action authz.Action, access authz.Access) error {
	if err := authz.AllowCollection(DeviceFrom(ctx), action, access); err != nil {
		return webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return nil
}
