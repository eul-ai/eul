package responses

import "encoding/json"

type streamedToolCall struct {
	id        string
	name      string
	arguments string
	complete  bool
}

type toolCallAccumulator struct {
	streams map[int]streamedToolCall
}

func newToolCallAccumulator() toolCallAccumulator {
	return toolCallAccumulator{streams: make(map[int]streamedToolCall)}
}

func (calls *toolCallAccumulator) start(index int, raw json.RawMessage) (streamedToolCall, bool) {
	var item outputItem
	if len(raw) == 0 || json.Unmarshal(raw, &item) != nil || item.Type != "function_call" {
		return streamedToolCall{}, false
	}
	if item.CallID == "" || item.Name == "" {
		return streamedToolCall{}, false
	}

	streamed := streamedToolCall{id: item.CallID, name: item.Name, arguments: item.Arguments}
	calls.streams[index] = streamed
	return streamed, true
}

func (calls *toolCallAccumulator) update(index int, delta, arguments string, complete bool) (streamedToolCall, bool) {
	streamed, exists := calls.streams[index]
	if !exists {
		return streamedToolCall{}, false
	}

	previousArguments := streamed.arguments
	if complete {
		if arguments != "" {
			streamed.arguments = arguments
		}
		if streamed.complete && streamed.arguments == previousArguments {
			return streamedToolCall{}, false
		}
		streamed.complete = true
	} else {
		streamed.arguments += delta
		if streamed.arguments == previousArguments {
			return streamedToolCall{}, false
		}
	}
	calls.streams[index] = streamed
	return streamed, true
}

func (calls *toolCallAccumulator) finish(index int, raw json.RawMessage) (streamedToolCall, bool) {
	var item outputItem
	if json.Unmarshal(raw, &item) != nil || item.Type != "function_call" {
		return streamedToolCall{}, false
	}

	streamed, exists := calls.streams[index]
	if !exists {
		streamed = streamedToolCall{id: item.CallID, name: item.Name}
	}
	previousArguments := streamed.arguments
	wasComplete := streamed.complete
	if item.CallID != "" {
		streamed.id = item.CallID
	}
	if item.Name != "" {
		streamed.name = item.Name
	}
	if item.Arguments != "" {
		streamed.arguments = item.Arguments
	}
	delete(calls.streams, index)

	if streamed.id == "" || streamed.name == "" || wasComplete && streamed.arguments == previousArguments {
		return streamedToolCall{}, false
	}
	return streamed, true
}
