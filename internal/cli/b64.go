package cli

import (
	"encoding/base64"
)

func b64(b []byte) string {
	return base64.RawStdEncoding.EncodeToString(b)
}

func b64in(s string) ([]byte, error) {
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
