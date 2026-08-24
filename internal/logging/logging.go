package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

type Options struct {
	Level             string
	SensitiveMetadata bool
	Writer            io.Writer
}

func New(opts Options) *slog.Logger {
	w := opts.Writer
	if w == nil {
		w = os.Stderr
	}
	var lvl slog.Level
	switch strings.ToLower(opts.Level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case "password", "token", "authorization", "sig", "signature",
				"pairing_code", "code", "secret", "key", "private_key",
				"app_password", "master_key":
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	})
	l := slog.New(h)
	if opts.SensitiveMetadata {
		l = l.With("sensitive_metadata", true)
	}
	return l
}
