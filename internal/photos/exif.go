package photos

import (
	"bytes"
	"encoding/binary"
	"time"
)

type exifInfo struct {
	Orientation int
	Make        string
	Model       string
	TakenAt     time.Time
	HasGPS      bool
}

func parseJPEGExif(b []byte) exifInfo {
	var out exifInfo
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return out
	}
	i := 2
	for i+4 < len(b) {
		if b[i] != 0xff {
			break
		}
		for i < len(b) && b[i] == 0xff {
			i++
		}
		if i >= len(b) {
			break
		}
		marker := b[i]
		i++
		if marker == 0xda || marker == 0xd9 {
			break
		}
		if i+2 > len(b) {
			break
		}
		n := int(b[i])<<8 | int(b[i+1])
		i += 2
		if n < 2 || i+n-2 > len(b) {
			break
		}
		payload := b[i : i+n-2]
		i += n - 2
		if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			out = parseTIFFExif(payload[6:])
			break
		}
	}
	return out
}

func parseTIFFExif(t []byte) exifInfo {
	var out exifInfo
	if len(t) < 8 {
		return out
	}
	var bo binary.ByteOrder
	switch {
	case t[0] == 'I' && t[1] == 'I':
		bo = binary.LittleEndian
	case t[0] == 'M' && t[1] == 'M':
		bo = binary.BigEndian
	default:
		return out
	}
	if bo.Uint16(t[2:4]) != 42 {
		return out
	}
	ifd0 := int(bo.Uint32(t[4:8]))
	readIFD(t, bo, ifd0, &out, true)
	return out
}

func readIFD(t []byte, bo binary.ByteOrder, off int, out *exifInfo, top bool) {
	if off < 0 || off+2 > len(t) {
		return
	}
	n := int(bo.Uint16(t[off : off+2]))
	p := off + 2
	var exifOff, gpsOff int
	for i := 0; i < n; i++ {
		if p+12 > len(t) {
			return
		}
		tag := bo.Uint16(t[p : p+2])
		typ := bo.Uint16(t[p+2 : p+4])
		cnt := bo.Uint32(t[p+4 : p+8])
		val := t[p+8 : p+12]
		p += 12
		switch tag {
		case 0x010f:
			out.Make = tiffString(t, bo, typ, cnt, val)
		case 0x0110:
			out.Model = tiffString(t, bo, typ, cnt, val)
		case 0x0112:
			if typ == 3 {
				out.Orientation = int(bo.Uint16(val[:2]))
			}
		case 0x0132:
			if out.TakenAt.IsZero() {
				out.TakenAt = parseExifTime(tiffString(t, bo, typ, cnt, val))
			}
		case 0x9003:
			out.TakenAt = parseExifTime(tiffString(t, bo, typ, cnt, val))
		case 0x8769:
			exifOff = int(bo.Uint32(val))
		case 0x8825:
			gpsOff = int(bo.Uint32(val))
			out.HasGPS = true
		}
	}
	if top && exifOff > 0 {
		readIFD(t, bo, exifOff, out, false)
	}
	if top && gpsOff > 0 {
		out.HasGPS = true
	}
}

func tiffString(t []byte, bo binary.ByteOrder, typ uint16, cnt uint32, val []byte) string {
	if typ != 2 || cnt == 0 {
		return ""
	}
	var raw []byte
	if cnt <= 4 {
		raw = val[:min(int(cnt), 4)]
	} else {
		off := int(bo.Uint32(val))
		if off < 0 || off+int(cnt) > len(t) {
			return ""
		}
		raw = t[off : off+int(cnt)]
	}
	raw = bytes.TrimRight(raw, "\x00")
	return string(bytes.TrimSpace(raw))
}

func parseExifTime(s string) time.Time {
	t, err := time.Parse("2006:01:02 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func jpegAPP1(b []byte) []byte {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return nil
	}
	i := 2
	for i+4 < len(b) {
		if b[i] != 0xff {
			return nil
		}
		for i < len(b) && b[i] == 0xff {
			i++
		}
		if i >= len(b) {
			return nil
		}
		marker := b[i]
		i++
		if marker == 0xda || marker == 0xd9 {
			return nil
		}
		if i+2 > len(b) {
			return nil
		}
		n := int(b[i])<<8 | int(b[i+1])
		i += 2
		if n < 2 || i+n-2 > len(b) {
			return nil
		}
		payload := b[i : i+n-2]
		if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			out := make([]byte, 2+n)
			out[0], out[1] = 0xff, 0xe1
			out[2] = byte(n >> 8)
			out[3] = byte(n)
			copy(out[4:], payload)
			return out
		}
		i += n - 2
	}
	return nil
}

func insertJPEGAPP1(jpeg, app1 []byte) []byte {
	if len(jpeg) < 2 || jpeg[0] != 0xff || jpeg[1] != 0xd8 || len(app1) == 0 {
		return jpeg
	}
	out := make([]byte, 0, len(jpeg)+len(app1))
	out = append(out, 0xff, 0xd8)
	out = append(out, app1...)
	out = append(out, jpeg[2:]...)
	return out
}
