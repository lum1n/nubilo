package syncengine_test

import (
	"encoding/json"
	"testing"

	"nubilo/internal/syncengine"
)

func FuzzChangeInputJSON(f *testing.F) {
	f.Add([]byte(`{"object_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","collection_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","op":"create","metadata":{}}`))
	f.Add([]byte(`{"op":"delete"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var in syncengine.ChangeInput
		_ = json.Unmarshal(raw, &in)
		var req struct {
			IdempotencyKey string                   `json:"idempotency_key"`
			Changes        []syncengine.ChangeInput `json:"changes"`
		}
		_ = json.Unmarshal(raw, &req)
		e := setup(t)
		if len(req.Changes) == 0 {
			req.Changes = []syncengine.ChangeInput{in}
		}
		if req.IdempotencyKey == "" {
			req.IdempotencyKey = "fuzz-key"
		}
		if len(req.Changes) > 20 {
			req.Changes = req.Changes[:20]
		}
		_, _ = e.eng.Push(e.ctx, e.dev, req.IdempotencyKey, req.Changes)
	})
}
