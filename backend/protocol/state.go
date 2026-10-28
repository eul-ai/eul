package protocol

import (
	"encoding/json"
	"fmt"
)

const version = 1

type state struct {
	Version int               `json:"version"`
	Items   []json.RawMessage `json:"items"`
}

func DecodeState(encoded []byte) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}

	var decoded state
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if decoded.Version != version {
		return nil, fmt.Errorf("unsupported continuation state version %d", decoded.Version)
	}
	return decoded.Items, nil
}

func EncodeState(groups ...[]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(state{Version: version, Items: join(groups...)})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
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
