package photos_test

import (
	"testing"

	"nubilo/internal/photos"
)

func FuzzInspect(f *testing.F) {
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe0})
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte("GIF89a"))
	f.Add([]byte("RIFF....WEBP"))
	f.Fuzz(func(t *testing.T, b []byte) {
		info := photos.Inspect(b)
		_ = info.MIME
		_ = photos.DetectMIME(b)
	})
}
