package continuation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	DefaultMaximumBytes   = 16 * 1024 * 1024
	envelopeBytes         = len(`{"version":1,"items":[]}`)
	defaultOutputHeadroom = 1024 * 1024
	version               = 1
)

type state struct {
	Version int               `json:"version"`
	Items   []json.RawMessage `json:"items"`
}

func Decode(encoded []byte, maximum int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}

	var decoded state
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if decoded.Version != version {
		return nil, fmt.Errorf("unsupported continuation state version %d", decoded.Version)
	}
	for index, item := range decoded.Items {
		if err := validateObject(item); err != nil {
			return nil, fmt.Errorf("continuation state item %d: %w", index, err)
		}
	}

	return decoded.Items, nil
}

func Encode(maximum int, groups ...[]json.RawMessage) ([]byte, error) {
	items := join(groups...)
	for index, item := range items {
		if err := validateObject(item); err != nil {
			return nil, fmt.Errorf("continuation state item %d: %w", index, err)
		}
	}

	encoded, err := json.Marshal(state{Version: version, Items: items})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}

	return encoded, nil
}

func join(groups ...[]json.RawMessage) []json.RawMessage {
	total := 0
	for _, group := range groups {
		total += len(group)
	}

	items := make([]json.RawMessage, 0, total)
	for _, group := range groups {
		items = append(items, group...)
	}
	return items
}

func validateObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errors.New("must be a JSON object")
	}
	return nil
}

func GenerationStateBytes(maximum, outputHeadroom int) int {
	if outputHeadroom <= 0 {
		outputHeadroom = min(defaultOutputHeadroom, maximum/4)
	}
	outputHeadroom = min(outputHeadroom, max(0, maximum-envelopeBytes))
	return max(0, maximum-outputHeadroom)
}
