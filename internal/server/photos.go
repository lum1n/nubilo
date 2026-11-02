package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	s.writeListJSON(w, r, map[string]any{"photos": out, "next": next})
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
	obj = s.photos().WithSourceDerivatives(r.Context(), obj)
	if rendition == "meta" {
		s.writeJSON(w, http.StatusOK, photoJSON(obj))
		return
	}
	hash, _, err := photos.RenditionBlob(obj, rendition)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	mime, body, err := s.photos().Blob(obj, rendition)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Blobs never change, so a URL naming the blob it wants can be kept for
	// good. The apps add ?v=<hash> from the photo's metadata; a stale v (a
	// thumbnail attached since) gets the new bytes and must be revalidated.
	if v := r.URL.Query().Get("v"); v != "" && strings.EqualFold(v, hash) {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Header().Set("Content-Type", mime)
	// The blob actually sent: a thumbnail attached later must not revalidate
	// as the original that stood in for it.
	w.Header().Set("ETag", `"`+hash+`"`)
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
		ThumbBlob     string `json:"thumb_blob"`
		PreviewBlob   string `json:"preview_blob"`
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
		ThumbBlob: req.ThumbBlob, PreviewBlob: req.PreviewBlob,
	})
	if errors.Is(err, photos.ErrForbidden) {
		http.Error(w, "forbidden", http.StatusForbidden)
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
	s.writeListJSON(w, r, map[string]any{"items": out, "next": next})
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

// handlePhotoIndex lists the caller's whole library in one compact response.
func (s *Server) handlePhotoIndex(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosRead); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	entries, err := s.photos().LibraryIndex(r.Context(), dev.UserID)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	s.writeListJSON(w, r, map[string]any{"photos": entries})
}

// handleAlbumIndex lists every item of an album in one compact response.
func (s *Server) handleAlbumIndex(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	entries, err := s.photos().AlbumIndex(r.Context(), dev, r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.writeListJSON(w, r, map[string]any{"photos": entries})
}

// handlePhotoDerivatives attaches a client-made thumbnail and preview to a
// photo the server could not make them for.
func (s *Server) handlePhotoDerivatives(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite); err != nil || dev.UserID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ThumbBlob   string `json:"thumb_blob"`
		PreviewBlob string `json:"preview_blob"`
	}
	if err := s.readJSON(r, &req); err != nil || (req.ThumbBlob == "" && req.PreviewBlob == "") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	obj, err := s.photos().AttachDerivatives(r.Context(), dev, r.PathValue("id"), req.ThumbBlob, req.PreviewBlob)
	switch {
	case errors.Is(err, syncengine.ErrNotFound), errors.Is(err, photos.ErrNotPhoto):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, photos.ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, photos.ErrBadDerivative):
		http.Error(w, "bad derivative", http.StatusBadRequest)
	case err != nil:
		http.Error(w, "internal", http.StatusInternalServerError)
	default:
		s.writeJSON(w, http.StatusOK, photoJSON(obj))
	}
}

// writeListJSON writes a (possibly large) JSON listing with a validator, so
// an app that has it already gets 304 instead of the body, and gzips it for
// clients that accept that: photo listings shrink to about a fifth.
func (s *Server) writeListJSON(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	// Weak: the same listing is sent gzipped or not.
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Vary", "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	if len(body) < 1024 || !acceptsGzip(r) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	h.Set("Content-Encoding", "gzip")
	w.WriteHeader(http.StatusOK)
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	_, _ = gz.Write(body)
	_ = gz.Close()
}

// etagMatches compares If-None-Match to etag the weak way, as RFC 9110 asks
// for If-None-Match.
func etagMatches(header, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == want {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		k, v, _ := strings.Cut(strings.TrimSpace(params), "=")
		if q, err := strconv.ParseFloat(strings.TrimSpace(v), 64); strings.TrimSpace(k) == "q" && err == nil && q == 0 {
			return false
		}
		return true
	}
	return false
}
