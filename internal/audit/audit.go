package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"nubilo/internal/store"
)

type Logger struct {
	Store *store.Store
	Slog  *slog.Logger
}

func (l *Logger) Event(ctx context.Context, deviceID, event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		b = []byte(`{"error":"marshal"}`)
	}
	if l.Store != nil {
		_, _ = l.Store.DB.ExecContext(ctx, `INSERT INTO audit(ts, device_id, event, fields) VALUES (?, ?, ?, ?)`,
			store.NowMS(), deviceID, event, string(b))
	}
	if l.Slog != nil {
		l.Slog.Info("audit", "event", event, "device_id", deviceID)
	}
}
