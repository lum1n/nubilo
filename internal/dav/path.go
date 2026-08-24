package dav

import (
	"errors"
	"strings"
)

const Prefix = "/dav"

var ErrBadPath = errors.New("dav: invalid path")

// Normalize rejects traversal and returns a cleaned absolute path.
func Normalize(p string) (string, error) {
	if p == "" {
		p = "/"
	}
	if strings.ContainsRune(p, 0) || strings.Contains(p, "\\") {
		return "", ErrBadPath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return "", ErrBadPath
		}
		if len(s) > 255 {
			return "", ErrBadPath
		}
		if strings.Contains(s, "\\") {
			return "", ErrBadPath
		}
		out = append(out, s)
	}
	if len(out) > 24 {
		return "", ErrBadPath
	}
	if len(out) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(out, "/"), nil
}

func Split(p string) ([]string, error) {
	n, err := Normalize(p)
	if err != nil {
		return nil, err
	}
	if n == "/" {
		return nil, nil
	}
	return strings.Split(n[1:], "/"), nil
}

func Join(elem ...string) string {
	parts := make([]string, 0, len(elem))
	for _, e := range elem {
		e = strings.Trim(e, "/")
		if e == "" {
			continue
		}
		parts = append(parts, e)
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

func HasPrefix(path, prefix string) bool {
	path = strings.TrimSuffix(path, "/")
	prefix = strings.TrimSuffix(prefix, "/")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
