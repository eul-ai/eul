package chatcompletions

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
	result, err := readCompletionSSE(fragmentedReader{reader: strings.NewReader(stream)}, agent.StreamObserver{
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
	if result.Text != "answer " || text != result.Text || reasoning != "think " {
		t.Fatalf("result=%+v text=%q reasoning=%q", result, text, reasoning)
	}
	if result.Usage != (agent.Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if len(result.ToolCalls) != 2 || result.ToolCalls[0].ID != "call_1" || result.ToolCalls[0].Name != "read" || string(result.ToolCalls[0].Arguments) != `{"p":"a"}` || result.ToolCalls[1].ID != "call_2" || string(result.ToolCalls[1].Arguments) != `{"p":"b"}` {
		t.Fatalf("calls = %+v", result.ToolCalls)
	}
	if len(snapshots) != 5 || !snapshots[len(snapshots)-1].Complete {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	if !strings.Contains(string(result.Output[0]), `"reasoning_content":"think "`) || !strings.Contains(string(result.Output[0]), `"tool_calls"`) {
		t.Fatalf("assistant = %s", result.Output[0])
	}
}

func TestReadCompletionSSEStreamsRefusal(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"refusal\":\"cannot help\"},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n"
	var delivered string
	result, err := readCompletionSSE(strings.NewReader(stream), agent.StreamObserver{Text: func(delta string) error {
		delivered += delta
		return nil
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "cannot help" || delivered != result.Text || !strings.Contains(string(result.Output[0]), "cannot help") {
		t.Fatalf("result=%+v delivered=%q assistant=%s", result, delivered, result.Output[0])
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
	result, err := readCompletionSSE(strings.NewReader(stream), agent.StreamObserver{ToolCall: func(snapshot agent.ToolCallSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call_1" || result.ToolCalls[0].Name != "read" || string(result.ToolCalls[0].Arguments) != `{"path":"a"}` {
		t.Fatalf("calls = %+v", result.ToolCalls)
	}
	if len(snapshots) != 2 || snapshots[0].Complete || !snapshots[1].Complete {
		t.Fatalf("snapshots = %+v", snapshots)
	}
}

func TestReadCompletionSSERejectsLegacyFunctionCallFinish(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"function_call\"}]}\n\ndata: [DONE]\n\n"
	if _, err := readCompletionSSE(strings.NewReader(stream), agent.StreamObserver{}, false); err == nil {
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
			result, err := readCompletionSSE(strings.NewReader(stream), agent.StreamObserver{}, test.serialize)
			if err != nil {
				t.Fatal(err)
			}
			var assistant map[string]json.RawMessage
			if err := json.Unmarshal(result.Output[0], &assistant); err != nil {
				t.Fatal(err)
			}
			reasoning, exists := assistant["reasoning_content"]
			if exists != test.wantField || exists && string(reasoning) != `""` {
				t.Fatalf("assistant = %s", result.Output[0])
			}
		})
	}
}

func TestReadCompletionSSERequiresFinishReason(t *testing.T) {
	_, err := readCompletionSSE(strings.NewReader("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"), agent.StreamObserver{}, false)
	if !errors.Is(err, backendhttp.ErrIncompleteStream) {
		t.Fatalf("error = %v", err)
	}
	if _, retry := backendhttp.RetryGeneration(err, 1); !retry {
		t.Fatal("incomplete stream is not retryable")
	}
}

func TestReadCompletionSSERetriesAfterDelivery(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	_, err := readCompletionSSE(strings.NewReader(stream), agent.StreamObserver{Text: func(string) error { return nil }}, false)
	if !errors.Is(err, backendhttp.ErrIncompleteStream) {
		t.Fatalf("error = %v", err)
	}
	if _, retry := backendhttp.RetryGeneration(err, 1); !retry {
		t.Fatal("partial stream is not retryable")
	}
}
