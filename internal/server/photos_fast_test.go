package server_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"testing"
	"time"

	"nubilo/internal/auth"
)

// heicBody is an original the server cannot decode, like an iPhone photo.
func heicBody(seed byte) []byte {
	b := append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), bytes.Repeat([]byte{seed}, 4096)...)
	return b
}

func jpegOfSize(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// raw sends a signed request with extra headers and returns the response
// with its body read.
func (c *signer) raw(method, path string, header http.Header) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.ts.URL+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", auth.SignRequest(c.priv, c.dev.ID, method, path, nil, time.Now().UnixMilli(), nonce()))
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestClientDerivatives(t *testing.T) {
	f := startFamily(t)
	alice := f.alice
	thumb, preview := jpegOfSize(t, 256, 192), jpegOfSize(t, 1280, 960)
	for _, b := range [][]byte{thumb, preview} {
		if code, _ := putBlob(alice, b); code != http.StatusOK {
			t.Fatalf("put derivative %d", code)
		}
	}
	_, thumbID := putBlob(alice, thumb)
	_, previewID := putBlob(alice, preview)

	// Commit with derivatives: the server keeps the client's JPEGs.
	_, orig := putBlob(alice, heicBody(1))
	p := alice.json(http.MethodPost, "/api/v1/photos/commit", map[string]string{
		"blob_id": orig, "name": "IMG_1.HEIC", "thumb_blob": thumbID, "preview_blob": previewID,
	}, http.StatusCreated)
	if p["thumb_hash"] != thumbID || p["preview_hash"] != previewID {
		t.Fatalf("commit derivatives %v", p)
	}
	if code, got := alice.do(http.MethodGet, "/api/v1/photos/"+p["id"].(string)+"/thumb", nil); code != 200 || !bytes.Equal(got, thumb) {
		t.Fatalf("thumb %d len %d", code, len(got))
	}

	// Commit without: the thumbnail falls back to the original until one is attached.
	heic2 := heicBody(2)
	_, orig2 := putBlob(alice, heic2)
	p2 := alice.json(http.MethodPost, "/api/v1/photos/commit", map[string]string{"blob_id": orig2, "name": "IMG_2.HEIC"}, http.StatusCreated)
	id2 := p2["id"].(string)
	if _, ok := p2["thumb_hash"]; ok {
		t.Fatalf("server made a thumb for HEIC: %v", p2)
	}
	if _, got := alice.do(http.MethodGet, "/api/v1/photos/"+id2+"/thumb", nil); !bytes.Equal(got, heic2) {
		t.Fatal("thumb fallback is not the original")
	}

	// Album made before the backfill: its item picks up the photo's thumbnail.
	album := alice.json(http.MethodPost, "/api/v1/albums", map[string]string{"name": "Trip"}, http.StatusCreated)
	albumID := album["id"].(string)
	alice.json(http.MethodPost, "/api/v1/albums/"+albumID+"/items", map[string]any{"photo_ids": []string{id2}}, http.StatusOK)

	// Only JPEGs within the limits.
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	_, pngID := putBlob(alice, pngBuf.Bytes())
	_, bigID := putBlob(alice, jpegOfSize(t, 600, 10))
	for _, bad := range []string{pngID, bigID} {
		if code, out := alice.do(http.MethodPost, "/api/v1/photos/"+id2+"/derivatives", map[string]string{"thumb_blob": bad}); code != http.StatusBadRequest {
			t.Fatalf("bad derivative accepted: %d %s", code, out)
		}
	}
	// Only blobs the user uploaded.
	other := jpegOfSize(t, 100, 100)
	putBlob(f.bob, other)
	_, otherID := putBlob(f.bob, other)
	if code, _ := alice.do(http.MethodPost, "/api/v1/photos/"+id2+"/derivatives", map[string]string{"thumb_blob": otherID}); code != http.StatusForbidden {
		t.Fatalf("someone else's blob: %d", code)
	}
	// Only the user's own photos.
	if code, _ := f.bob.do(http.MethodPost, "/api/v1/photos/"+id2+"/derivatives", map[string]string{"thumb_blob": otherID}); code != http.StatusNotFound {
		t.Fatalf("bob attached to alice's photo: %d", code)
	}

	got := alice.json(http.MethodPost, "/api/v1/photos/"+id2+"/derivatives", map[string]string{"thumb_blob": thumbID, "preview_blob": previewID}, http.StatusOK)
	if got["thumb_hash"] != thumbID || got["preview_hash"] != previewID {
		t.Fatalf("attach %v", got)
	}
	// Existing derivatives are kept.
	_, thumb2ID := putBlob(alice, jpegOfSize(t, 200, 100))
	got = alice.json(http.MethodPost, "/api/v1/photos/"+id2+"/derivatives", map[string]string{"thumb_blob": thumb2ID}, http.StatusOK)
	if got["thumb_hash"] != thumbID {
		t.Fatalf("thumb replaced: %v", got)
	}

	// Album index and item thumbnail come from the photo.
	index := alice.json(http.MethodGet, "/api/v1/albums/"+albumID+"/index", nil, http.StatusOK)
	items := index["photos"].([]any)
	item := items[0].(map[string]any)
	if len(items) != 1 || item["thumb_hash"] != thumbID || item["photo_id"] != id2 {
		t.Fatalf("album index %v", index)
	}
	if code, b := alice.do(http.MethodGet, "/api/v1/photos/"+item["id"].(string)+"/thumb", nil); code != 200 || !bytes.Equal(b, thumb) {
		t.Fatalf("album item thumb %d len %d", code, len(b))
	}
}

func TestPhotoIndexAndCaching(t *testing.T) {
	f := startFamily(t)
	alice := f.alice
	img := jpegBody(t)
	if code, out := alice.do(http.MethodPost, "/api/v1/photos?name=a.jpg", img); code != http.StatusCreated {
		t.Fatalf("upload %d %s", code, out)
	}
	for i := byte(0); i < 30; i++ {
		_, h := putBlob(alice, heicBody(10+i))
		alice.json(http.MethodPost, "/api/v1/photos/commit", map[string]any{"blob_id": h, "name": "IMG.HEIC", "taken_at": 1_700_000_000_000 + int64(i)}, http.StatusCreated)
	}

	resp, body := alice.raw(http.MethodGet, "/api/v1/photos/index", http.Header{"Accept-Encoding": {"gzip"}})
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("index %d encoding %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || len(body) == 0 {
		t.Fatal("no etag or body")
	}
	var index struct {
		Photos []map[string]any `json:"photos"`
	}
	if code, b := alice.do(http.MethodGet, "/api/v1/photos/index", nil); code != 200 || json.Unmarshal(b, &index) != nil {
		t.Fatalf("index %d", code)
	}
	if len(index.Photos) != 31 {
		t.Fatalf("index has %d photos", len(index.Photos))
	}
	var jpegEntry map[string]any
	for _, p := range index.Photos {
		if p["thumb_hash"] != nil {
			jpegEntry = p
		}
		for _, k := range []string{"phash", "camera_make", "collection_id", "revision"} {
			if _, ok := p[k]; ok {
				t.Fatalf("index entry carries %s: %v", k, p)
			}
		}
	}
	if jpegEntry == nil || jpegEntry["checksum"] == nil {
		t.Fatalf("no jpeg entry with thumb and checksum: %v", index.Photos)
	}

	if resp, _ := alice.raw(http.MethodGet, "/api/v1/photos/index", http.Header{"If-None-Match": {etag}}); resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidate %d", resp.StatusCode)
	}
	// A change gives a new listing.
	_, h := putBlob(alice, heicBody(99))
	alice.json(http.MethodPost, "/api/v1/photos/commit", map[string]any{"blob_id": h, "name": "NEW.HEIC"}, http.StatusCreated)
	if resp, _ := alice.raw(http.MethodGet, "/api/v1/photos/index", http.Header{"If-None-Match": {etag}}); resp.StatusCode != 200 {
		t.Fatalf("changed library still %d", resp.StatusCode)
	}

	// Renditions named by hash can be cached for good.
	id, hash := jpegEntry["id"].(string), jpegEntry["thumb_hash"].(string)
	resp, _ = alice.raw(http.MethodGet, "/api/v1/photos/"+id+"/thumb?v="+hash, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("versioned thumb %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("ETag") != `"`+hash+`"` {
		t.Fatalf("thumb etag %q is not the thumbnail's hash", resp.Header.Get("ETag"))
	}
	for _, path := range []string{"/thumb", "/thumb?v=0000", "/original?v=" + hash} {
		resp, _ = alice.raw(http.MethodGet, "/api/v1/photos/"+id+path, nil)
		if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, no-cache" {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
	resp, _ = alice.raw(http.MethodGet, "/api/v1/photos/"+id+"/original?v="+jpegEntry["checksum"].(string), nil)
	if resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("versioned original %q", resp.Header.Get("Cache-Control"))
	}

	// Bob's index is his own.
	bob := f.bob.json(http.MethodGet, "/api/v1/photos/index", nil, http.StatusOK)
	if n := len(bob["photos"].([]any)); n != 0 {
		t.Fatalf("bob's index has %d photos", n)
	}
}
