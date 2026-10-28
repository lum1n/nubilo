package server_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"
)

func pushNamed(s *signer, colID, name string, payload []byte) string {
	s.t.Helper()
	_, hash := putBlob(s, payload)
	id := newULID()
	res := s.json(http.MethodPost, "/sync/v1/push", map[string]any{
		"idempotency_key": nonce(),
		"changes": []map[string]any{{
			"object_id": id, "collection_id": colID, "kind": "file", "op": "create",
			"content_hash": hash, "blob_id": hash, "size": len(payload), "metadata": map[string]string{"name": name},
		}},
	}, http.StatusOK)
	if st := pushStatus(res); st != "ok" {
		s.t.Fatalf("push %s: %s", name, st)
	}
	return id
}

func TestFileThumbnails(t *testing.T) {
	f := startFamily(t)
	alice, bob := f.alice, f.bob
	files := f.files(t, alice)

	src := image.NewRGBA(image.Rect(0, 0, 600, 300))
	for x := 0; x < 600; x++ {
		for y := 0; y < 300; y++ {
			src.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	pic := pushNamed(alice, files, "red.png", buf.Bytes())
	txt := pushNamed(alice, files, "notes.txt", []byte("not a picture"))

	// The owner gets a JPEG, scaled to the size asked for (rounded up).
	code, out := alice.do(http.MethodPost, "/sync/v1/thumb", map[string]any{"id": pic, "size": 100})
	if code != http.StatusOK {
		t.Fatalf("owner thumb: %d %s", code, out)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 128 || b.Dy() != 64 {
		t.Fatalf("thumb size %v", b)
	}

	// Non-images are refused; unknown objects are not found.
	if code, _ := alice.do(http.MethodPost, "/sync/v1/thumb", map[string]any{"id": txt}); code != http.StatusUnsupportedMediaType {
		t.Fatalf("text thumb: %d", code)
	}
	if code, _ := alice.do(http.MethodPost, "/sync/v1/thumb", map[string]any{"id": newULID()}); code != http.StatusNotFound {
		t.Fatalf("missing object: %d", code)
	}

	// Bob cannot read Alice's files, even though the thumbnail is cached now.
	if code, _ := bob.do(http.MethodPost, "/sync/v1/thumb", map[string]any{"id": pic, "size": 100}); code != http.StatusNotFound {
		t.Fatalf("other user got thumb: %d", code)
	}

	// After a read-only share he can.
	alice.json(http.MethodPut, "/api/v1/collections/"+files+"/shares", map[string]any{"username": "bob", "access": "ro"}, http.StatusOK)
	if code, out := bob.do(http.MethodPost, "/sync/v1/thumb", map[string]any{"id": pic}); code != http.StatusOK || !bytes.HasPrefix(out, []byte{0xff, 0xd8}) {
		t.Fatalf("ro share thumb: %d", code)
	}
}
