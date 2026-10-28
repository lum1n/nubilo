package server

import (
	"container/list"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"nubilo/internal/authz"
	"nubilo/internal/photos"
)

// File thumbnails for the Files app and Finder: a JPEG of an image file the
// device can read, made on demand from its blob.
//
// Thumbnails are cached in memory by content hash and size. Blobs are
// deduplicated across users, so the cache is only consulted after the
// access check on the object; a hit never reveals more than the caller
// could fetch with /sync/v1/blob.

// thumbSizes are the sizes made; a request is rounded up to the next one,
// so the cache holds at most a few renditions per image.
var thumbSizes = []int{64, 128, 256, 512, 1024}

func thumbSize(want int) int {
	if want <= 0 {
		return 256
	}
	for _, s := range thumbSizes {
		if want <= s {
			return s
		}
	}
	return thumbSizes[len(thumbSizes)-1]
}

var (
	// thumbCache is shared by all servers in the process; the key is the
	// content hash, so one blob gives the same thumbnail everywhere.
	thumbCache = newLRUBytes(32 << 20)
	// thumbSlots bounds concurrent decodes (each may take ~256 MB at
	// photos.MaxDecodePixels).
	thumbSlots = make(chan struct{}, 2)
)

// handleThumb returns a JPEG thumbnail of one file object:
// {"id", "size"?} → image/jpeg. 404 when the object is missing or not
// readable, 415 when it is not an image the server decodes, 413 when it is
// larger than the server's blob limit.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if err := authz.Allow(dev, authz.SyncRead); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ID   string `json:"id"`
		Size int    `json:"size"`
	}
	if err := s.readJSON(r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	obj, err := s.Engine.GetObject(ctx, req.ID)
	if err != nil || obj.DeletedAt != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if access, err := s.Engine.AccessFor(ctx, dev, obj.CollectionID); err != nil || access < authz.AccessRead {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	hash := obj.BlobID
	if hash == "" {
		hash = obj.ContentHash
	}
	if obj.Kind != "file" || hash == "" {
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}
	if max := s.Cfg.Sync.MaxBlobBytes; max > 0 && obj.Size > max {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	size := thumbSize(req.Size)
	key := hash + "/" + strconv.Itoa(size)
	jpg, ok := thumbCache.get(key)
	if !ok {
		select {
		case thumbSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		jpg, err = s.makeThumb(hash, size)
		<-thumbSlots
		switch {
		case errors.Is(err, errBlobMissing):
			http.Error(w, "not found", http.StatusNotFound)
			return
		case errors.Is(err, errBlobTooLarge):
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		case err != nil:
			// Not decodable: remember that too (an empty entry).
			jpg = nil
		}
		thumbCache.put(key, jpg)
	}
	if len(jpg) == 0 {
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(jpg)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(jpg)
}

var (
	errBlobMissing  = errors.New("blob missing")
	errBlobTooLarge = errors.New("blob too large")
)

func (s *Server) makeThumb(hash string, size int) ([]byte, error) {
	pt, err := s.Store.GetBlobPlaintext(hash)
	if err != nil {
		return nil, errBlobMissing
	}
	if max := s.Cfg.Sync.MaxBlobBytes; max > 0 && int64(len(pt)) > max {
		return nil, errBlobTooLarge
	}
	return photos.Thumbnail(pt, size)
}

// lruBytes is a byte-bounded LRU cache of byte slices. Empty values are
// allowed (negative entries) and count a small fixed cost.
type lruBytes struct {
	mu    sync.Mutex
	limit int
	used  int
	order *list.List // front = most recent
	items map[string]*list.Element
}

type lruEntry struct {
	key string
	val []byte
}

func newLRUBytes(limit int) *lruBytes {
	return &lruBytes{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

func entryCost(e *lruEntry) int { return len(e.key) + len(e.val) + 64 }

func (c *lruBytes) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

func (c *lruBytes) put(key string, val []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := &lruEntry{key: key, val: val}
	if entryCost(e) > c.limit {
		return
	}
	if el, ok := c.items[key]; ok {
		c.used -= entryCost(el.Value.(*lruEntry))
		c.order.Remove(el)
	}
	c.items[key] = c.order.PushFront(e)
	c.used += entryCost(e)
	for c.used > c.limit {
		el := c.order.Back()
		old := el.Value.(*lruEntry)
		c.order.Remove(el)
		delete(c.items, old.key)
		c.used -= entryCost(old)
	}
}
