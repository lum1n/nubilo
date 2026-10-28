package photos

import (
	"bytes"
	"errors"
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

var (
	// ErrNotImage means the bytes are not in a format the server decodes
	// (JPEG, PNG, GIF, WebP, BMP, TIFF; not HEIC, RAW or video).
	ErrNotImage = errors.New("photos: not a decodable image")
	// ErrImageTooLarge means the image header claims more pixels than
	// MaxDecodePixels; it is not decoded.
	ErrImageTooLarge = errors.New("photos: image too large to decode")
)

// MaxDecodePixels bounds what Thumbnail decodes, so a small file that
// claims a huge canvas (a decompression bomb) cannot exhaust memory:
// 64 MP is about 256 MB as RGBA, more than any phone camera produces.
const MaxDecodePixels = 64 << 20

// Thumbnail decodes an image and returns a JPEG no larger than maxPx on its
// longer side, upright (EXIF orientation applied) and flattened onto white.
// The image header is checked against MaxDecodePixels before decoding.
func Thumbnail(original []byte, maxPx int) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(original))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxDecodePixels {
		return nil, ErrImageTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		return nil, ErrNotImage
	}
	ori := 1
	if format == "jpeg" {
		if ex := parseJPEGExif(original); ex.Orientation >= 1 && ex.Orientation <= 8 {
			ori = ex.Orientation
		}
	}
	// Resize first: orienting the small image is cheaper.
	return encodeJPEG(applyOrientation(flattenMax(img, maxPx), ori), 80)
}

// flattenMax scales src to fit maxPx (never up) onto an opaque white canvas,
// so transparent PNG and GIF areas do not turn black in the JPEG.
func flattenMax(src image.Image, maxPx int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if maxPx > 0 && (w > maxPx || h > maxPx) {
		if w >= h {
			nw, nh = maxPx, h*maxPx/w
		} else {
			nw, nh = w*maxPx/h, maxPx
		}
	}
	nw, nh = max(nw, 1), max(nh, 1)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if nw == w && nh == h {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	}
	return dst
}
