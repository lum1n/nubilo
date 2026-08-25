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
	case isDNG(b):
		return "image/dng"
	case isMP4(b):
		return "video/mp4"
	case isQuickTime(b):
		return "video/quicktime"
	default:
		return "application/octet-stream"
	}
}

func isDNG(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	// TIFF/DNG little- or big-endian with DNGVersion tag often present later;
	// treat TIFF magic + "DNG" somewhere in first 256 bytes as DNG.
	le := b[0] == 'I' && b[1] == 'I' && b[2] == 42 && b[3] == 0
	be := b[0] == 'M' && b[1] == 'M' && b[2] == 0 && b[3] == 42
	if !le && !be {
		return false
	}
	n := min(len(b), 256)
	return bytes.Contains(b[:n], []byte("DNG")) || bytes.Contains(b[:n], []byte("Adobe"))
}

func isMP4(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	brand := string(b[8:12])
	switch brand {
	case "isom", "iso2", "mp41", "mp42", "avc1", "M4V ", "MSNV":
		return true
	}
	return bytes.Contains(b[8:min(len(b), 64)], []byte("mp4"))
}

func isQuickTime(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	if string(b[4:8]) == "ftyp" {
		brand := string(b[8:12])
		return brand == "qt  " || brand == "havc"
	}
	// Classic QuickTime often starts with a size + "moov"/"mdat"/"wide"
	typ := string(b[4:8])
	return typ == "moov" || typ == "mdat" || typ == "wide" || typ == "free"
}

func KindFromMIME(mime, hint string) string {
	switch hint {
	case "video", "live", "raw", "image":
		return hint
	}
	switch {
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case mime == "image/dng" || strings.Contains(mime, "raw"):
		return "raw"
	default:
		return "image"
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
	case "image/dng":
		ext = ".dng"
	case "video/mp4":
		ext = ".mp4"
	case "video/quicktime":
		ext = ".mov"
	}
	return "photo" + ext
}
