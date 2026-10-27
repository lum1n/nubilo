package server

import (
	"errors"
	"net/http"
	"strings"

	"nubilo/internal/authz"
	"nubilo/internal/dav"
	"nubilo/internal/syncengine"
)

// Folder operations for signed devices (the iOS Files app integration).
// They follow the same rules as WebDAV (internal/dav/fs.go): top-level
// folders need owner access to rename, move or delete; subfolders need
// write access; nothing moves between users.

// handleObjects lists the live objects of one collection, a page at a time.
func (s *Server) handleObjects(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		CollectionID string `json:"collection_id"`
		AfterID      string `json:"after_id"`
		Limit        int    `json:"limit"`
	}
	if err := s.readJSON(r, &req); err != nil || req.CollectionID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if access, err := s.Engine.AccessFor(r.Context(), dev, req.CollectionID); err != nil || access < authz.AccessRead {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 1000
	}
	objs, err := s.Engine.ListObjectsPage(r.Context(), req.CollectionID, "", req.AfterID, req.Limit)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if objs == nil {
		objs = []syncengine.Object{} // [] rather than null for empty folders
	}
	next := ""
	if len(objs) == req.Limit {
		next = objs[len(objs)-1].ID
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"objects": objs, "next_after_id": next})
}

// handleObject returns one live object the device can read.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := s.readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	obj, err := s.Engine.GetObject(r.Context(), req.ID)
	if err != nil || obj.DeletedAt != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if access, err := s.Engine.AccessFor(r.Context(), dev, obj.CollectionID); err != nil || access < authz.AccessRead {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, obj)
}

// handleCollectionUpdate renames and/or moves a collection. parent_id ""
// with "root": true makes it top-level.
func (s *Server) handleCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := s.readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	col, ok := s.collectionForAdmin(w, r, req.ID)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = col.Name
	}
	if !dav.ValidDisplayName(name) {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	parentID := col.ParentID
	if req.ParentID != nil {
		parentID = *req.ParentID
	}
	if parentID != "" {
		if access, err := s.Engine.AccessFor(ctx, dev, parentID); err != nil || access < authz.AccessWrite {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		parent, err := s.Engine.GetCollection(ctx, parentID)
		if err != nil || parent.DeletedAt != nil || parent.Kind != col.Kind {
			http.Error(w, "bad parent", http.StatusBadRequest)
			return
		}
		if parent.OwnerUserID != col.OwnerUserID {
			http.Error(w, "cannot move between users", http.StatusForbidden)
			return
		}
		if s.Engine.IsAncestor(ctx, col.ID, parentID) {
			http.Error(w, "cannot move a folder into itself", http.StatusConflict)
			return
		}
	} else if col.OwnerUserID != dev.UserID {
		http.Error(w, "cannot move between users", http.StatusForbidden)
		return
	}
	if name != col.Name || parentID != col.ParentID {
		if other, err := s.Engine.FindChildCollection(ctx, col.OwnerUserID, col.Kind, parentID, name); err == nil && other.ID != col.ID {
			http.Error(w, "a folder with that name exists", http.StatusConflict)
			return
		}
	}
	if name != col.Name {
		if err := s.Engine.RenameCollection(ctx, col.ID, name); err != nil {
			http.Error(w, "rename failed", http.StatusConflict)
			return
		}
	}
	if parentID != col.ParentID {
		if err := s.Engine.SetCollectionParent(ctx, col.ID, parentID); err != nil {
			http.Error(w, "move failed", http.StatusConflict)
			return
		}
	}
	updated, err := s.Engine.GetCollection(ctx, col.ID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, updated)
}

// handleCollectionDelete deletes a collection with its subfolders and objects.
func (s *Server) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := s.readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	col, ok := s.collectionForAdmin(w, r, req.ID)
	if !ok {
		return
	}
	if err := s.Engine.DeleteCollectionTree(r.Context(), dev, col); err != nil {
		if errors.Is(err, syncengine.ErrTreeConflict) {
			http.Error(w, "some items could not be deleted", http.StatusConflict)
			return
		}
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// collectionForAdmin loads a live collection the device may rename, move or
// delete: owner access for top-level collections, write access below.
func (s *Server) collectionForAdmin(w http.ResponseWriter, r *http.Request, id string) (*syncengine.Collection, bool) {
	dev := deviceFrom(r)
	col, err := s.Engine.GetCollection(r.Context(), id)
	if err != nil || col.DeletedAt != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	access, err := s.Engine.AccessFor(r.Context(), dev, id)
	if err != nil || access == authz.AccessNone {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	want := authz.AccessWrite
	if col.ParentID == "" {
		want = authz.AccessOwner
	}
	if access < want {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return col, true
}
