package responses

import (
	"encoding/json"
	"testing"
)

func TestCodexContinuationStateCompatibility(t *testing.T) {
	encoded := []byte(`{"version":1,"items":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}]}`)
	items, err := decodeState(encoded, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	reencoded, err := encodeState(nil, nil, items, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(reencoded) || string(reencoded) != string(encoded) {
		t.Fatalf("reencoded state = %s", reencoded)
	}
}
