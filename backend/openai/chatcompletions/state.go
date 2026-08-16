package chatcompletions

import (
	"encoding/json"

	"github.com/eul-ai/eul/backend/continuation"
)

const continuationStateEnvelopeBytes = continuation.MessagesEnvelopeBytes

func decodeState(encoded []byte, maximum int) ([]json.RawMessage, error) {
	return continuation.DecodeMessages(encoded, maximum)
}

func encodeState(history, newMessages, output []json.RawMessage, maximum int) ([]byte, error) {
	return continuation.EncodeMessages(maximum, history, newMessages, output)
}
