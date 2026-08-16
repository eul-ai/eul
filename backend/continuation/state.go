package continuation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	MessagesEnvelopeBytes = len(`{"version":1,"messages":[]}`)
	defaultOutputHeadroom = 1024 * 1024
	version               = 1
)

type messagesState struct {
	Version  int               `json:"version"`
	Messages []json.RawMessage `json:"messages"`
}

func DecodeMessages(encoded []byte, maximum int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}

	var state messagesState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if state.Version != version {
		return nil, fmt.Errorf("unsupported continuation state version %d", state.Version)
	}
	for index, message := range state.Messages {
		if err := ValidateRawObject(message); err != nil {
			return nil, fmt.Errorf("continuation state message %d: %w", index, err)
		}
	}

	return state.Messages, nil
}

func EncodeMessages(maximum int, groups ...[]json.RawMessage) ([]byte, error) {
	messages := RawMessages(groups...)
	for index, message := range messages {
		if err := ValidateRawObject(message); err != nil {
			return nil, fmt.Errorf("continuation state message %d: %w", index, err)
		}
	}

	encoded, err := json.Marshal(messagesState{Version: version, Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}

	return encoded, nil
}

func RawMessages(groups ...[]json.RawMessage) []json.RawMessage {
	total := 0
	for _, group := range groups {
		total += len(group)
	}

	messages := make([]json.RawMessage, 0, total)
	for _, group := range groups {
		messages = append(messages, group...)
	}
	return messages
}

func ValidateRawObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errors.New("must be a JSON object")
	}
	return nil
}

func GenerationStateBytes(maximum, outputHeadroom, envelopeBytes int) int {
	if outputHeadroom <= 0 {
		outputHeadroom = min(defaultOutputHeadroom, maximum/4)
	}
	outputHeadroom = min(outputHeadroom, max(0, maximum-envelopeBytes))
	return max(0, maximum-outputHeadroom)
}
