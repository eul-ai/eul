package messages

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type fragmentedReader struct {
	reader io.Reader
}

func (reader fragmentedReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return reader.reader.Read(buffer)
}

func TestReadMessagesSSEStreamsAndPreservesBlocks(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(&wireUsage{
			InputTokens:              int64Pointer(8),
			OutputTokens:             int64Pointer(0),
			CacheReadInputTokens:     int64Pointer(2),
			CacheCreationInputTokens: int64Pointer(1),
		}),
		sseContentBlockStart(t, 0, contentBlock{Type: "thinking"}),
		sseContentBlockDelta(0, streamDelta{Type: "thinking_delta", Thinking: "consider "}),
		sseContentBlockDelta(0, streamDelta{Type: "signature_delta", Signature: "signed"}),
		sseContentBlockStop(0),
		sseContentBlockStart(t, 1, contentBlock{Type: "text"}),
		sseContentBlockDelta(1, streamDelta{Type: "text_delta", Text: "answer"}),
		sseContentBlockStop(1),
		sseContentBlockStart(t, 2, contentBlock{Type: "tool_use", ID: "call_1", Name: "read", Input: json.RawMessage(`{}`)}),
		sseContentBlockDelta(2, streamDelta{Type: "input_json_delta", PartialJSON: `{"path":"a"}`}),
		sseContentBlockStop(2),
		sseMessageDelta("tool_use", &wireUsage{OutputTokens: int64Pointer(5)}),
		sseMessageStop(),
	)

	var text, reasoning string
	var snapshots []agent.ToolCallSnapshot
	result, err := readMessagesSSE(fragmentedReader{reader: strings.NewReader(stream)}, 1<<20, agent.StreamObserver{
		Text: func(delta string) error {
			text += delta
			return nil
		},
		Reasoning: func(delta string) error {
			reasoning += delta
			return nil
		},
		ToolCall: func(snapshot agent.ToolCallSnapshot) error {
			snapshots = append(snapshots, snapshot)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "answer" || text != result.text || reasoning != "consider " {
		t.Fatalf("result=%+v text=%q reasoning=%q", result, text, reasoning)
	}
	if result.usage != (agent.Usage{InputTokens: 11, OutputTokens: 5, TotalTokens: 16}) {
		t.Fatalf("usage = %+v", result.usage)
	}
	if len(result.calls) != 1 || result.calls[0].ID != "call_1" || result.calls[0].Name != "read" || string(result.calls[0].Arguments) != `{"path":"a"}` {
		t.Fatalf("calls = %+v", result.calls)
	}
	if len(snapshots) != 3 || !snapshots[len(snapshots)-1].Complete {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	if !strings.Contains(string(result.assistant), `"signature":"signed"`) || !strings.Contains(string(result.assistant), `"tool_use"`) {
		t.Fatalf("assistant = %s", result.assistant)
	}
}

func TestReadMessagesSSEAcceptsRefusal(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(nil),
		sseContentBlockStart(t, 0, contentBlock{Type: "text", Text: "cannot help"}),
		sseContentBlockStop(0),
		sseMessageDelta("refusal", nil),
		sseMessageStop(),
	)
	result, err := readMessagesSSE(strings.NewReader(stream), 1024, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "cannot help" {
		t.Fatalf("text = %q", result.text)
	}
}

func TestReadMessagesSSEClassifiesContextWindowStop(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(nil),
		sseMessageDelta("model_context_window_exceeded", nil),
		sseMessageStop(),
	)
	_, err := readMessagesSSE(strings.NewReader(stream), 1024, agent.StreamObserver{})
	if err == nil || !contextLimitError(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadMessagesSSEOrdersMultipleToolCalls(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(nil),
		sseContentBlockStart(t, 1, contentBlock{Type: "tool_use", ID: "call_2", Name: "write", Input: json.RawMessage(`{"path":"b"}`)}),
		sseContentBlockStop(1),
		sseContentBlockStart(t, 0, contentBlock{Type: "tool_use", ID: "call_1", Name: "read", Input: json.RawMessage(`{"path":"a"}`)}),
		sseContentBlockStop(0),
		sseMessageDelta("tool_use", nil),
		sseMessageStop(),
	)
	result, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.calls) != 2 || result.calls[0].ID != "call_1" || result.calls[1].ID != "call_2" {
		t.Fatalf("calls = %+v", result.calls)
	}
}

func TestReadMessagesSSERejectsMalformedToolInput(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(nil),
		sseContentBlockStart(t, 0, contentBlock{Type: "tool_use", ID: "call_1", Name: "read", Input: json.RawMessage(`{}`)}),
		sseContentBlockDelta(0, streamDelta{Type: "input_json_delta", PartialJSON: "{"}),
		sseContentBlockStop(0),
		sseMessageDelta("tool_use", nil),
		sseMessageStop(),
	)
	if _, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{}); err == nil {
		t.Fatal("malformed tool input was accepted")
	}
}

func TestReadMessagesSSERetriesAfterDelivery(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(nil),
		sseContentBlockStart(t, 0, contentBlock{Type: "text"}),
		sseContentBlockDelta(0, streamDelta{Type: "text_delta", Text: "partial"}),
	)
	_, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{Text: func(string) error { return nil }})
	var partial *backendhttp.PartialResponseError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); !retry {
		t.Fatal("partial stream is not retryable")
	}
}

func TestReadMessagesSSERetriesWhenNothingWasDelivered(t *testing.T) {
	stream := marshalSSE(
		t,
		sseMessageStart(nil),
		sseContentBlockStart(t, 0, contentBlock{Type: "text"}),
		sseContentBlockDelta(0, streamDelta{Type: "text_delta", Text: "partial"}),
	)
	_, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{})
	var partial *backendhttp.PartialResponseError
	if errors.As(err, &partial) {
		t.Fatalf("undelivered response was marked partial: %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); !retry {
		t.Fatal("undelivered incomplete response is not retryable")
	}
}
