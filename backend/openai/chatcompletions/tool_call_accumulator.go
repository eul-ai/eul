package chatcompletions

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/eul-ai/eul/agent"
)

type streamedToolCall struct {
	id        string
	name      string
	arguments string
}

type finalizedToolCall struct {
	streamed streamedToolCall
	call     agent.ToolCall
	wire     toolCall
}

type toolCallAccumulator struct {
	calls map[int]streamedToolCall
}

func newToolCallAccumulator() toolCallAccumulator {
	return toolCallAccumulator{calls: make(map[int]streamedToolCall)}
}

func (calls *toolCallAccumulator) apply(delta toolCallDelta) streamedToolCall {
	call := calls.calls[delta.Index]
	if delta.ID != "" {
		call.id = delta.ID
	}
	if delta.Function.Name != "" {
		call.name = delta.Function.Name
	}
	call.arguments += delta.Function.Arguments
	calls.calls[delta.Index] = call
	return call
}

func (calls *toolCallAccumulator) finalize() ([]finalizedToolCall, error) {
	indexes := make([]int, 0, len(calls.calls))
	for index := range calls.calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	finalized := make([]finalizedToolCall, 0, len(indexes))
	seen := make(map[string]struct{}, len(indexes))
	for _, index := range indexes {
		streamed := calls.calls[index]
		if streamed.id == "" || streamed.name == "" {
			return nil, fmt.Errorf("chat completion tool call %d is incomplete", index)
		}
		if _, exists := seen[streamed.id]; exists {
			return nil, fmt.Errorf("chat completion has duplicate tool call ID %q", streamed.id)
		}
		seen[streamed.id] = struct{}{}

		arguments := streamed.arguments
		if arguments == "" {
			arguments = "{}"
		}
		finalized = append(finalized, finalizedToolCall{
			streamed: streamed,
			call: agent.ToolCall{
				ID:        streamed.id,
				Name:      streamed.name,
				Arguments: json.RawMessage(arguments),
			},
			wire: toolCall{
				ID:   streamed.id,
				Type: "function",
				Function: toolFunction{
					Name:      streamed.name,
					Arguments: arguments,
				},
			},
		})
	}
	return finalized, nil
}
