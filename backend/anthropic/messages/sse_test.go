package messages

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
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
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":8,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":0}}}`,
		"",
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"consider "}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`,
		"",
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		"",
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		"",
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		"",
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_1","name":"read","input":{}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}`,
		"",
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":2}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

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
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{}}`, "",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"cannot help"}}`, "",
		`data: {"type":"content_block_stop","index":0}`, "",
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal"}}`, "",
		`data: {"type":"message_stop"}`, "",
	}, "\n")
	result, err := readMessagesSSE(strings.NewReader(stream), 1024, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "cannot help" {
		t.Fatalf("text = %q", result.text)
	}
}

func TestReadMessagesSSEClassifiesContextWindowStop(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{}}`, "",
		`data: {"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"}}`, "",
		`data: {"type":"message_stop"}`, "",
	}, "\n")
	_, err := readMessagesSSE(strings.NewReader(stream), 1024, agent.StreamObserver{})
	if err == nil || !contextLimitError(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadMessagesSSEOrdersMultipleToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{}}`, "",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_2","name":"write","input":{"path":"b"}}}`, "",
		`data: {"type":"content_block_stop","index":1}`, "",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"read","input":{"path":"a"}}}`, "",
		`data: {"type":"content_block_stop","index":0}`, "",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, "",
		`data: {"type":"message_stop"}`, "",
	}, "\n")
	result, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.calls) != 2 || result.calls[0].ID != "call_1" || result.calls[1].ID != "call_2" {
		t.Fatalf("calls = %+v", result.calls)
	}
}

func TestReadMessagesSSERejectsMalformedToolInput(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{}}`, "",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"read","input":{}}}`, "",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}`, "",
		`data: {"type":"content_block_stop","index":0}`, "",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, "",
		`data: {"type":"message_stop"}`, "",
	}, "\n")
	if _, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{}); err == nil {
		t.Fatal("malformed tool input was accepted")
	}
}

func TestReadMessagesSSEDoesNotRetryAfterDelivery(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{}}`, "",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, "",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`, "",
	}, "\n")
	_, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{Text: func(string) error { return nil }})
	var partial *partialResponseError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); retry {
		t.Fatal("partial stream is retryable")
	}
}

func TestReadMessagesSSERetriesWhenNothingWasDelivered(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{}}`, "",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, "",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`, "",
	}, "\n")
	_, err := readMessagesSSE(strings.NewReader(stream), 1<<20, agent.StreamObserver{})
	var partial *partialResponseError
	if errors.As(err, &partial) {
		t.Fatalf("undelivered response was marked partial: %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); !retry {
		t.Fatal("undelivered incomplete response is not retryable")
	}
}
