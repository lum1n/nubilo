package server_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"testing"
	"time"

	"nubilo/internal/auth"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
)

func jpegBody(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 10, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPhotosAPI(t *testing.T) {
	ts, idsvc, _ := start(t)
	pub, priv, err := ncrypto.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	dev, err := idsvc.Enroll(context.Background(), "mac", pub, identity.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	orig := jpegBody(t)
	path := "/api/v1/photos?name=shot.jpg"
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(orig))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, http.MethodPost, path, orig, time.Now().UnixMilli(), nonce()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload %d %s", resp.StatusCode, b)
	}
	var meta map[string]any
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	if _, ok := meta["gps"]; ok {
		t.Fatal("gps in json")
	}
	if _, ok := meta["lat"]; ok {
		t.Fatal("lat in json")
	}
	id, _ := meta["id"].(string)
	if id == "" {
		t.Fatalf("%v", meta)
	}

	origPath := "/api/v1/photos/" + id + "/original"
	get, _ := http.NewRequest(http.MethodGet, ts.URL+origPath, nil)
	get.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, http.MethodGet, origPath, nil, time.Now().UnixMilli(), nonce()))
	resp, err = http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, orig) {
		t.Fatalf("original %d len=%d", resp.StatusCode, len(got))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges=%q", resp.Header.Get("Accept-Ranges"))
	}

	rangeReq, _ := http.NewRequest(http.MethodGet, ts.URL+origPath, nil)
	rangeReq.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, http.MethodGet, origPath, nil, time.Now().UnixMilli(), nonce()))
	rangeReq.Header.Set("Range", "bytes=0-9")
	resp, err = http.DefaultClient.Do(rangeReq)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(part, orig[:10]) {
		t.Fatalf("range %d body=%x want=%x", resp.StatusCode, part, orig[:10])
	}

	thumbPath := "/api/v1/photos/" + id + "/thumb"
	thumb, _ := http.NewRequest(http.MethodGet, ts.URL+thumbPath, nil)
	thumb.Header.Set("Authorization", auth.SignRequest(priv, dev.ID, http.MethodGet, thumbPath, nil, time.Now().UnixMilli(), nonce()))
	resp, err = http.DefaultClient.Do(thumb)
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(tb) == 0 || bytes.Equal(tb, orig) {
		t.Fatalf("thumb %d len=%d", resp.StatusCode, len(tb))
	}

	unauth, err := http.Get(ts.URL + "/api/v1/photos")
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth %d", unauth.StatusCode)
	}

	davDev, pass, err := idsvc.CreateDAVDevice(context.Background(), "files-only", "webdav")
	if err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/photos", nil)
	req.SetBasicAuth(davDev.ID, pass)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("webdav photos %d", resp.StatusCode)
	}

	photoDev, photoPass, err := idsvc.CreateDAVDevice(context.Background(), "gallery", "photos")
	if err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/photos", nil)
	req.SetBasicAuth(photoDev.ID, photoPass)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("photos scope %d %s", resp.StatusCode, lb)
	}
}

func nonce() string {
	n, _ := ncrypto.Random(16)
	return hex.EncodeToString(n)
}
