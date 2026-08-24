package photos

import (
	"bytes"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

type Options struct {
	StripGPSFromDerivatives bool
	PerceptualHash          bool
	ThumbMaxPx              int
	PreviewMaxPx            int
}

func DefaultOptions() Options {
	return Options{
		StripGPSFromDerivatives: true,
		ThumbMaxPx:              256,
		PreviewMaxPx:            1280,
	}
}

func (o Options) withDefaults() Options {
	if o.ThumbMaxPx <= 0 {
		o.ThumbMaxPx = 256
	}
	if o.PreviewMaxPx <= 0 {
		o.PreviewMaxPx = 1280
	}
	return o
}

type Prepared struct {
	Original []byte
	Preview  []byte
	Thumb    []byte
	Meta     Meta
}

// Prepare inspects original bytes, builds preview/thumbnail JPEGs, and fills metadata.
// The original slice is never mutated.
func Prepare(original []byte, name string, opt Options) (Prepared, error) {
	opt = opt.withDefaults()
	info := Inspect(original)
	name = Filename(name, info.MIME)
	p := Prepared{
		Original: original,
		Meta: Meta{
			Name:        name,
			MIME:        info.MIME,
			Width:       info.Width,
			Height:      info.Height,
			Orientation: info.Orientation,
			CameraMake:  info.CameraMake,
			CameraModel: info.CameraModel,
			TakenAtMS:   info.TakenAtMS,
			HasGPS:      info.HasGPS,
		},
	}
	img, err := Decode(original)
	if err != nil {
		return p, nil
	}
	img = applyOrientation(img, info.Orientation)
	if opt.PerceptualHash {
		p.Meta.Perceptual = dHash(img)
	}
	thumb, err := encodeJPEG(resizeMax(img, opt.ThumbMaxPx), 70)
	if err != nil {
		return Prepared{}, err
	}
	preview, err := encodeJPEG(resizeMax(img, opt.PreviewMaxPx), 85)
	if err != nil {
		return Prepared{}, err
	}
	if !opt.StripGPSFromDerivatives && info.MIME == "image/jpeg" {
		if app1 := jpegAPP1(original); app1 != nil {
			preview = insertJPEGAPP1(preview, app1)
			thumb = insertJPEGAPP1(thumb, app1)
		}
	}
	p.Thumb = thumb
	p.Preview = preview
	return p, nil
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func resizeMax(src image.Image, maxPx int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxPx <= 0 || (w <= maxPx && h <= maxPx) {
		return src
	}
	nw, nh := w, h
	if w >= h {
		nw = maxPx
		nh = h * maxPx / w
	} else {
		nh = maxPx
		nw = w * maxPx / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

func applyOrientation(img image.Image, ori int) image.Image {
	switch ori {
	case 2:
		return flipH(img)
	case 3:
		return rotate180(img)
	case 4:
		return flipV(img)
	case 5:
		return rotate90(flipH(img))
	case 6:
		return rotate90(img)
	case 7:
		return rotate270(flipH(img))
	case 8:
		return rotate270(img)
	default:
		return img
	}
}

func rotate90(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(b.Max.Y-1-y, x-b.Min.X, src.At(x, y))
		}
	}
	return dst
}

func rotate180(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(b.Max.X-1-x, b.Max.Y-1-y, src.At(x, y))
		}
	}
	return dst
}

func rotate270(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(y-b.Min.Y, b.Max.X-1-x, src.At(x, y))
		}
	}
	return dst
}

func flipH(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(b.Max.X-1-x, y-b.Min.Y, src.At(x, y))
		}
	}
	return dst
}

func flipV(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x-b.Min.X, b.Max.Y-1-y, src.At(x, y))
		}
	}
	return dst
}
