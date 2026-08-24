package photos

import (
	"fmt"
	"image"
	"image/color"
)

// dHash is a 64-bit difference hash (9x8 grayscale). It is optional metadata, not identity.
func dHash(img image.Image) string {
	const w, h = 9, 8
	small := image.NewGray(image.Rect(0, 0, w, h))
	b := img.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*b.Dx()/w
			sy := b.Min.Y + y*b.Dy()/h
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			if sy >= b.Max.Y {
				sy = b.Max.Y - 1
			}
			r, g, bv, _ := img.At(sx, sy).RGBA()
			y8 := uint8((299*r + 587*g + 114*bv) / 1000 >> 8)
			small.SetGray(x, y, color.Gray{Y: y8})
		}
	}
	var bits uint64
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			if small.GrayAt(x, y).Y < small.GrayAt(x+1, y).Y {
				bits |= 1 << uint(63-i)
			}
			i++
		}
	}
	return fmt.Sprintf("%016x", bits)
}
