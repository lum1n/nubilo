package syncengine_test

import (
	"encoding/json"
	"testing"

	"nubilo/internal/identity"
	"nubilo/internal/syncengine"
)

func TestMirrorPatch(t *testing.T) {
	mac := &identity.Device{ID: "mac-app"}
	decode := func(raw json.RawMessage) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	on := decode(syncengine.MirrorPatch(json.RawMessage(`{"mirror":true,"mirror_device":"other","color":"#112233"}`), mac))
	if on["mirror_device"] != "mac-app" || on["color"] != "#112233" {
		t.Fatalf("turning on: %v", on)
	}
	// Without mirror:true a client cannot name the writer.
	other := decode(syncengine.MirrorPatch(json.RawMessage(`{"mirror_device":"other","color":"#112233"}`), mac))
	if _, ok := other["mirror_device"]; ok {
		t.Fatalf("mirror_device kept: %v", other)
	}
}
