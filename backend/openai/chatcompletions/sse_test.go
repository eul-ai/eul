package chatcompletions

import (
	"encoding/json"
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

func TestReadCompletionSSEStreamsAndNormalizes(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"answer "},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"write","arguments":"{\"p\":"}}]},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"b\"}"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var text, reasoning string
	var snapshots []agent.ToolCallSnapshot
	result, err := readCompletionSSE(fragmentedReader{reader: strings.NewReader(stream)}, 1<<20, agent.StreamObserver{
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
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "answer " || text != result.text || result.reasoning != "think " || reasoning != result.reasoning {
		t.Fatalf("result=%+v text=%q reasoning=%q", result, text, reasoning)
	}
	if result.usage != (agent.Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}) {
		t.Fatalf("usage = %+v", result.usage)
	}
	if len(result.calls) != 2 || result.calls[0].ID != "call_1" || result.calls[0].Name != "read" || string(result.calls[0].Arguments) != `{"p":"a"}` || result.calls[1].ID != "call_2" || string(result.calls[1].Arguments) != `{"p":"b"}` {
		t.Fatalf("calls = %+v", result.calls)
	}
	if len(snapshots) != 5 || !snapshots[len(snapshots)-1].Complete {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	if !strings.Contains(string(result.assistant), `"reasoning_content":"think "`) || !strings.Contains(string(result.assistant), `"tool_calls"`) {
		t.Fatalf("assistant = %s", result.assistant)
	}
}

func TestReadCompletionSSEStreamsRefusal(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"refusal\":\"cannot help\"},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n"
	var delivered string
	result, err := readCompletionSSE(strings.NewReader(stream), 1024, agent.StreamObserver{Text: func(delta string) error {
		delivered += delta
		return nil
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "cannot help" || delivered != result.text || !strings.Contains(string(result.assistant), "cannot help") {
		t.Fatalf("result=%+v delivered=%q assistant=%s", result, delivered, result.assistant)
	}
}

func TestReadCompletionSSEAcceptsSplitToolMetadata(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1"}]},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var snapshots []agent.ToolCallSnapshot
	result, err := readCompletionSSE(strings.NewReader(stream), 1024, agent.StreamObserver{ToolCall: func(snapshot agent.ToolCallSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.calls) != 1 || result.calls[0].ID != "call_1" || result.calls[0].Name != "read" || string(result.calls[0].Arguments) != `{"path":"a"}` {
		t.Fatalf("calls = %+v", result.calls)
	}
	if len(snapshots) != 2 || snapshots[0].Complete || !snapshots[1].Complete {
		t.Fatalf("snapshots = %+v", snapshots)
	}
}

func TestReadCompletionSSERejectsLegacyFunctionCallFinish(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"function_call\"}]}\n\ndata: [DONE]\n\n"
	if _, err := readCompletionSSE(strings.NewReader(stream), 1024, agent.StreamObserver{}, false); err == nil {
		t.Fatal("legacy function_call finish reason was accepted")
	}
}

func TestReadCompletionSSESerializesEmptyReasoningContentWhenRequested(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	for _, test := range []struct {
		name      string
		serialize bool
		wantField bool
	}{
		{name: "default"},
		{name: "required", serialize: true, wantField: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := readCompletionSSE(strings.NewReader(stream), 1024, agent.StreamObserver{}, test.serialize)
			if err != nil {
				t.Fatal(err)
			}
			var assistant map[string]json.RawMessage
			if err := json.Unmarshal(result.assistant, &assistant); err != nil {
				t.Fatal(err)
			}
			reasoning, exists := assistant["reasoning_content"]
			if exists != test.wantField || exists && string(reasoning) != `""` {
				t.Fatalf("assistant = %s", result.assistant)
			}
		})
	}
}

func TestReadCompletionSSERequiresFinishReason(t *testing.T) {
	_, err := readCompletionSSE(strings.NewReader("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"), 1024, agent.StreamObserver{}, false)
	if !errors.Is(err, errSSEIncomplete) {
		t.Fatalf("error = %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); !retry {
		t.Fatal("incomplete stream is not retryable")
	}
}

func TestReadCompletionSSEDoesNotRetryAfterDelivery(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	_, err := readCompletionSSE(strings.NewReader(stream), 1024, agent.StreamObserver{Text: func(string) error { return nil }}, false)
	var partial *partialResponseError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); retry {
		t.Fatal("partial stream is retryable")
	}
}

func TestReadCompletionSSERetriesWhenNothingWasDelivered(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	_, err := readCompletionSSE(strings.NewReader(stream), 1024, agent.StreamObserver{}, false)
	var partial *partialResponseError
	if errors.As(err, &partial) {
		t.Fatalf("undelivered response was marked partial: %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); !retry {
		t.Fatal("undelivered incomplete response is not retryable")
	}
}
