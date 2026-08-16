package messages

import (
	"encoding/json"
	"strings"
	"testing"
)

func marshalSSE(t testing.TB, events ...any) string {
	t.Helper()

	var stream strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		stream.WriteString("data: ")
		stream.Write(data)
		stream.WriteString("\n\n")
	}
	return stream.String()
}

func sseMessageStart(usage *wireUsage) streamEvent {
	return streamEvent{Type: "message_start", Message: &streamMessage{Usage: usage}}
}

func sseContentBlockStart(t testing.TB, index int, block contentBlock) streamEvent {
	t.Helper()
	content, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	return streamEvent{Type: "content_block_start", Index: index, ContentBlock: content}
}

func sseContentBlockDelta(index int, delta streamDelta) streamEvent {
	return streamEvent{Type: "content_block_delta", Index: index, Delta: delta}
}

func sseContentBlockStop(index int) streamEvent {
	return streamEvent{Type: "content_block_stop", Index: index}
}

func sseMessageDelta(stopReason string, usage *wireUsage) streamEvent {
	return streamEvent{Type: "message_delta", Delta: streamDelta{StopReason: stopReason}, Usage: usage}
}

func sseMessageStop() streamEvent {
	return streamEvent{Type: "message_stop"}
}

func int64Pointer(value int64) *int64 {
	return &value
}
