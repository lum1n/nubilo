package photos

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestThumbnailScalesAndFlattens(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 400, 200)) // fully transparent
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, err := Thumbnail(buf.Bytes(), 100)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("size %v", b)
	}
	if r, g, b, _ := img.At(50, 25).RGBA(); r < 0xf000 || g < 0xf000 || b < 0xf000 {
		t.Fatalf("transparent area not white: %x %x %x", r, g, b)
	}
}

func TestThumbnailRejects(t *testing.T) {
	if _, err := Thumbnail([]byte("plain text"), 128); !errors.Is(err, ErrNotImage) {
		t.Fatalf("text: %v", err)
	}
	// A GIF header claiming a 65535x65535 canvas is refused before decoding.
	bomb := []byte("GIF89a\xff\xff\xff\xff\x00\x00\x00;")
	if _, err := Thumbnail(bomb, 128); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("bomb: %v", err)
	}
}
