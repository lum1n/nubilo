package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"nubilo/internal/auth"
	"nubilo/internal/authz"
	"nubilo/internal/photos"
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
			dev, err = s.IDs.AuthenticatePassword(r.Context(), user, pass)
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
	if err := authz.Allow(dev, authz.PhotosRead, ""); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	objs, err := s.photos().List(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(objs))
	for i := range objs {
		out = append(out, photoJSON(&objs[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"photos": out})
}

func (s *Server) handlePhotosUpload(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.PhotosWrite, ""); err != nil {
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
	if err := authz.Allow(dev, authz.PhotosRead, ""); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	obj, err := s.photos().Get(r.Context(), r.PathValue("id"))
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
	return out
}
