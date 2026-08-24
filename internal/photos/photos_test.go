package photos_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/integrity"
	"nubilo/internal/photos"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInspectAndPrepare(t *testing.T) {
	orig := testJPEG(t, 400, 300)
	info := photos.Inspect(orig)
	if info.MIME != "image/jpeg" || info.Width != 400 || info.Height != 300 {
		t.Fatalf("%+v", info)
	}
	prep, err := photos.Prepare(orig, "shot.jpg", photos.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prep.Original, orig) {
		t.Fatal("original mutated")
	}
	if len(prep.Thumb) == 0 || len(prep.Preview) == 0 {
		t.Fatal("missing derivatives")
	}
	if bytes.Equal(prep.Thumb, orig) || bytes.Equal(prep.Preview, orig) {
		t.Fatal("derivative replaced original")
	}
	th := photos.Inspect(prep.Thumb)
	if th.Width > 256 || th.Height > 256 {
		t.Fatalf("thumb too large %+v", th)
	}
	pv := photos.Inspect(prep.Preview)
	if pv.Width > 1280 || pv.Height > 1280 {
		t.Fatalf("preview too large %+v", pv)
	}
	if photos.HasLatLon(photos.EncodeMeta(prep.Meta)) {
		t.Fatal("gps leaked into metadata")
	}
}

func TestPerceptualHash(t *testing.T) {
	a := testJPEG(t, 80, 80)
	opt := photos.DefaultOptions()
	opt.PerceptualHash = true
	pa, err := photos.Prepare(a, "a.jpg", opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(pa.Meta.Perceptual) != 16 {
		t.Fatalf("phash %q", pa.Meta.Perceptual)
	}
	pb, err := photos.Prepare(a, "b.jpg", opt)
	if err != nil {
		t.Fatal(err)
	}
	if pa.Meta.Perceptual != pb.Meta.Perceptual {
		t.Fatal("identical images should share phash")
	}
}

func TestIngestDedupAndNoGPS(t *testing.T) {
	dir := t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ := ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := syncengine.New(st)
	idsvc := identity.NewService(st)
	pub, _, _ := ncrypto.GenerateEd25519()
	dev, err := idsvc.Enroll(context.Background(), "cli", pub, identity.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	svc := photos.Service{Engine: eng, Store: st, Opt: photos.DefaultOptions()}
	orig := testJPEG(t, 120, 80)
	o1, err := svc.Ingest(context.Background(), dev, orig, "x.jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	o2, err := svc.Ingest(context.Background(), dev, orig, "x.jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o1.ID != o2.ID {
		t.Fatal("same name+hash should reuse object")
	}
	m := photos.ParseMeta(o1.Metadata)
	if m.Checksum != o1.ContentHash || m.Checksum == "" {
		t.Fatalf("checksum %+v hash %s", m, o1.ContentHash)
	}
	if photos.HasLatLon(o1.Metadata) {
		t.Fatal("gps in metadata")
	}
	pt, err := st.GetBlobPlaintext(o1.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, orig) {
		t.Fatal("stored original differs")
	}
	if m.ThumbHash == "" || m.PreviewHash == "" {
		t.Fatal("missing derivative hashes")
	}
	if m.ThumbHash == o1.BlobID || m.PreviewHash == o1.BlobID {
		t.Fatal("derivative hash equals original")
	}
	issues, err := integrity.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("verify after ingest: %v", issues)
	}
}
