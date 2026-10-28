package continuation

import (
	"encoding/json"
	"testing"
)

func TestStateRoundTripAndValidation(t *testing.T) {
	encoded, err := Encode(1024, []json.RawMessage{json.RawMessage(`{"role":"user"}`)})
	if err != nil {
		t.Fatal(err)
	}
	items, err := Decode(encoded, 1024)
	if err != nil || len(items) != 1 || string(items[0]) != `{"role":"user"}` {
		t.Fatalf("items = %s, error = %v", items, err)
	}
	if _, err := Decode([]byte(`{"version":2,"items":[]}`), 1024); err == nil {
		t.Fatal("unsupported version was accepted")
	}
	if _, err := Decode(encoded, len(encoded)-1); err == nil {
		t.Fatal("oversized state was accepted")
	}
}
