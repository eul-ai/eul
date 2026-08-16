package continuation

import (
	"encoding/json"
	"testing"
)

func TestMessagesRoundTripAndValidation(t *testing.T) {
	encoded, err := EncodeMessages(1024, []json.RawMessage{json.RawMessage(`{"role":"user"}`)})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := DecodeMessages(encoded, 1024)
	if err != nil || len(messages) != 1 || string(messages[0]) != `{"role":"user"}` {
		t.Fatalf("messages=%s err=%v", messages, err)
	}
	if _, err := DecodeMessages([]byte(`{"version":2,"messages":[]}`), 1024); err == nil {
		t.Fatal("unsupported version was accepted")
	}
	if _, err := EncodeMessages(1024, []json.RawMessage{json.RawMessage(`[]`)}); err == nil {
		t.Fatal("non-object message was accepted")
	}
	if _, err := DecodeMessages(encoded, len(encoded)-1); err == nil {
		t.Fatal("oversized state was accepted")
	}
}

func TestGenerationStateBytes(t *testing.T) {
	if got := GenerationStateBytes(1000, 0, MessagesEnvelopeBytes); got != 750 {
		t.Fatalf("default GenerationStateBytes() = %d", got)
	}
	if got := GenerationStateBytes(1000, 200, MessagesEnvelopeBytes); got != 800 {
		t.Fatalf("GenerationStateBytes() = %d", got)
	}
	if got := GenerationStateBytes(1000, 2000, MessagesEnvelopeBytes); got != MessagesEnvelopeBytes {
		t.Fatalf("bounded GenerationStateBytes() = %d", got)
	}
}
