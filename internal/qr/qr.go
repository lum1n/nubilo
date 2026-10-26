// Package qr renders invite and setup links as QR codes for the admin UI
// (PNG data URLs) and the terminal (half-block text).
package qr

import (
	"encoding/base64"

	qrcode "github.com/skip2/go-qrcode"
)

// PNGDataURL returns a data: URL of a PNG QR code for content.
func PNGDataURL(content string, size int) (string, error) {
	if size <= 0 {
		size = 320
	}
	png, err := qrcode.Encode(content, qrcode.Medium, size)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// Terminal returns content as a QR code drawn with Unicode half blocks,
// suitable for printing to a terminal and scanning with a phone camera.
func Terminal(content string) (string, error) {
	q, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return "", err
	}
	return q.ToSmallString(false), nil
}
