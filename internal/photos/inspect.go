package photos

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type Info struct {
	MIME        string
	Width       int
	Height      int
	Orientation int
	CameraMake  string
	CameraModel string
	TakenAtMS   int64
	HasGPS      bool
}

func DetectMIME(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "image/jpeg"
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case isHEIF(b):
		return "image/heic"
	default:
		return "application/octet-stream"
	}
}

func isHEIF(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	if string(b[4:8]) != "ftyp" {
		return false
	}
	brand := string(b[8:12])
	switch brand {
	case "heic", "heix", "hevc", "mif1", "msf1", "heim", "heis":
		return true
	}
	return bytes.Contains(b[8:min(len(b), 32)], []byte("heic")) || bytes.Contains(b[8:min(len(b), 32)], []byte("mif1"))
}

func Inspect(b []byte) Info {
	info := Info{MIME: DetectMIME(b), Orientation: 1}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
		info.Width = cfg.Width
		info.Height = cfg.Height
	}
	if info.MIME == "image/jpeg" {
		ex := parseJPEGExif(b)
		if ex.Orientation >= 1 && ex.Orientation <= 8 {
			info.Orientation = ex.Orientation
		}
		info.CameraMake = ex.Make
		info.CameraModel = ex.Model
		info.HasGPS = ex.HasGPS
		if !ex.TakenAt.IsZero() {
			info.TakenAtMS = ex.TakenAt.UnixMilli()
		}
	}
	return info
}

func Decode(b []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("photos: decode: %w", err)
	}
	return img, nil
}

func Filename(name, mime string) string {
	name = strings.TrimSpace(name)
	if ValidName(name) {
		return name
	}
	ext := ".bin"
	switch mime {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	case "image/heic":
		ext = ".heic"
	}
	return "photo" + ext
}
