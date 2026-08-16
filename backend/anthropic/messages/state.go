package messages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	continuationStateVersion       = 1
	continuationStateEnvelopeBytes = len(`{"version":1,"messages":[]}`)
)

type continuationState struct {
	Version  int               `json:"version"`
	Messages []json.RawMessage `json:"messages"`
}

func decodeState(encoded []byte, maximum int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}

	var state continuationState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if state.Version != continuationStateVersion {
		return nil, fmt.Errorf("unsupported continuation state version %d", state.Version)
	}
	for index, raw := range state.Messages {
		if err := validateRawObject(raw); err != nil {
			return nil, fmt.Errorf("continuation state message %d: %w", index, err)
		}
	}
	return state.Messages, nil
}

func encodeState(history, newMessages, output []json.RawMessage, maximum int) ([]byte, error) {
	messages := make([]json.RawMessage, 0, len(history)+len(newMessages)+len(output))
	messages = append(messages, history...)
	messages = append(messages, newMessages...)
	messages = append(messages, output...)
	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}
	return encoded, nil
}

func validateRawObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errors.New("must be a JSON object")
	}
	return nil
}
