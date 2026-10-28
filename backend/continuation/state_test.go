package continuation

import (
	"encoding/json"
	"testing"
)

func TestStateRoundTripAndVersion(t *testing.T) {
	encoded, err := Encode([]json.RawMessage{json.RawMessage(`{"role":"user"}`)})
	if err != nil {
		t.Fatal(err)
	}
	items, err := Decode(encoded)
	if err != nil || len(items) != 1 || string(items[0]) != `{"role":"user"}` {
		t.Fatalf("items = %s, error = %v", items, err)
	}
	if _, err := Decode([]byte(`{"version":2,"items":[]}`)); err == nil {
		t.Fatal("unsupported version was accepted")
	}
}
