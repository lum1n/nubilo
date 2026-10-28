package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nubilo/internal/auth"
	"nubilo/internal/authz"
	"nubilo/internal/photos"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

func (s *Server) photos() photos.Service {
	return photos.Service{
		Engine: s.Engine,
		Store:  s.Store,
		Opt: photos.Options{
			StripGPSFromDerivatives: s.Cfg.Photos.StripGPSFromDerivatives,
			PerceptualHash:          s.Cfg.Photos.PerceptualHash,
			ThumbMaxPx:              s.Cfg.Photos.ThumbMaxPx,
			PreviewMaxPx:            s.Cfg.Photos.PreviewMaxPx,
		},
	}
}

func (s *Server) authedAny(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, err := s.Auth.AuthenticateRequest(r)
		if err != nil {
			user, pass, ok := r.BasicAuth()
			if !ok || user == "" || pass == "" {
				if errors.Is(err, auth.ErrMissingAuth) {
					w.Header().Set("WWW-Authenticate", `Basic realm="nubilo"`)
				}
				s.rejectAuth(w, r, err)
				return
			}
			dev, err = s.IDs.AuthenticateBasic(r.Context(), user, pass)
			if err != nil {
				s.rejectAuth(w, r, err)
				return
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), deviceKey, dev)))
	}
}

func (s *Server) handlePhotosList(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosRead); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	objs, next, err := s.photos().List(r.Context(), dev.UserID, q.Get("after"), limit)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(objs))
	for i := range objs {
		out = append(out, photoJSON(&objs[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"photos": out, "next": next})
}

func (s *Server) handlePhotosUpload(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.Cfg.Sync.MaxBlobBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > s.Cfg.Sync.MaxBlobBytes {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.Header.Get("X-Nubilo-Name"))
	}
	obj, err := s.photos().Ingest(r.Context(), dev, body, name, nil)
	if errors.Is(err, photos.ErrQuotaExceeded) {
		http.Error(w, "quota exceeded", http.StatusInsufficientStorage)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusCreated, photoJSON(obj))
}

func (s *Server) handlePhotoMeta(w http.ResponseWriter, r *http.Request) {
	s.servePhoto(w, r, "meta")
}

func (s *Server) handlePhotoOriginal(w http.ResponseWriter, r *http.Request) {
	s.servePhoto(w, r, "original")
}

func (s *Server) handlePhotoPreview(w http.ResponseWriter, r *http.Request) {
	s.servePhoto(w, r, "preview")
}

func (s *Server) handlePhotoThumb(w http.ResponseWriter, r *http.Request) {
	s.servePhoto(w, r, "thumb")
}

func (s *Server) servePhoto(w http.ResponseWriter, r *http.Request, rendition string) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	obj, err := s.photos().Get(r.Context(), dev, r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if rendition == "meta" {
		s.writeJSON(w, http.StatusOK, photoJSON(obj))
		return
	}
	mime, body, err := s.photos().Blob(obj, rendition)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("ETag", `"`+obj.ContentHash+`"`)
	http.ServeContent(w, r, obj.ID+"-"+rendition, time.UnixMilli(obj.UpdatedAt), bytes.NewReader(body))
}

func photoJSON(o *syncengine.Object) map[string]any {
	m := photos.ParseMeta(o.Metadata)
	out := photos.PublicMeta(m)
	out["id"] = o.ID
	out["revision"] = o.Revision
	out["size"] = o.Size
	out["content_hash"] = o.ContentHash
	out["collection_id"] = o.CollectionID
	if o.Kind == photos.AlbumItemKind {
		out["photo_id"] = photos.AlbumItemPhotoID(o.Metadata)
	}
	return out
}

func (s *Server) handlePhotosCheck(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Hashes []string `json:"hashes"`
	}
	if err := s.readJSON(r, &req); err != nil || len(req.Hashes) > 1000 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	missing, err := s.photos().Missing(r.Context(), dev.UserID, req.Hashes)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"missing": missing})
}

func (s *Server) handlePhotosCommit(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		BlobID        string `json:"blob_id"`
		Name          string `json:"name"`
		TakenAt       int64  `json:"taken_at"`
		LocalID       string `json:"local_id"`
		LiveVideoBlob string `json:"live_video_blob"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.BlobID = strings.ToLower(req.BlobID)
	req.LiveVideoBlob = strings.ToLower(req.LiveVideoBlob)
	if !store.IsBlobID(req.BlobID) || (req.LiveVideoBlob != "" && !store.IsBlobID(req.LiveVideoBlob)) || len(req.LocalID) > 256 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	obj, err := s.photos().Commit(r.Context(), dev, photos.CommitRequest{
		BlobID: req.BlobID, Name: req.Name, TakenAtMS: req.TakenAt, LocalID: req.LocalID, LiveVideoBlob: req.LiveVideoBlob,
	})
	if errors.Is(err, photos.ErrForbidden) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if errors.Is(err, photos.ErrQuotaExceeded) {
		http.Error(w, "quota exceeded", http.StatusInsufficientStorage)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusCreated, photoJSON(obj))
}

func (s *Server) handleAlbums(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosRead); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	albums, err := s.photos().Albums(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"albums": albums})
}

func (s *Server) handleCreateAlbum(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := s.readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	a, err := s.photos().CreateAlbum(r.Context(), dev.UserID, req.Name)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleAlbumItems(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	objs, next, err := s.photos().AlbumItems(r.Context(), dev, r.PathValue("id"), q.Get("after"), limit)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	out := make([]map[string]any, 0, len(objs))
	for i := range objs {
		out = append(out, photoJSON(&objs[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": out, "next": next})
}

func (s *Server) handleAddAlbumItems(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		PhotoIDs []string `json:"photo_ids"`
	}
	if err := s.readJSON(r, &req); err != nil || len(req.PhotoIDs) == 0 || len(req.PhotoIDs) > 500 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	items, err := s.photos().AddToAlbum(r.Context(), dev, r.PathValue("id"), req.PhotoIDs)
	if errors.Is(err, photos.ErrForbidden) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, photoJSON(&items[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
