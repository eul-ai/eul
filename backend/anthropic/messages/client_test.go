package messages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientContinuesThinkingAndToolUse(t *testing.T) {
	requestNumber := 0
	client, err := New(Options{
		Endpoint: "https://example.test/v1/messages",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requestNumber++
			if request.Header.Get("x-api-key") != "secret" || request.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("headers = %v", request.Header)
			}
			var wire createRequest
			if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			if !wire.Stream || wire.MaxTokens != 32_000 {
				t.Fatalf("request options = %+v", wire)
			}

			var stream string
			switch requestNumber {
			case 1:
				if len(wire.Messages) != 1 || len(wire.System) != 1 {
					t.Fatalf("first request = %+v", wire)
				}
				stream = marshalSSE(
					t,
					sseMessageStart(&wireUsage{InputTokens: int64Pointer(10), OutputTokens: int64Pointer(0)}),
					sseContentBlockStart(t, 0, contentBlock{Type: "thinking"}),
					sseContentBlockDelta(0, streamDelta{Type: "thinking_delta", Thinking: "check "}),
					sseContentBlockDelta(0, streamDelta{Type: "signature_delta", Signature: "signed"}),
					sseContentBlockStop(0),
					sseContentBlockStart(t, 1, contentBlock{Type: "tool_use", ID: "call_1", Name: "read", Input: json.RawMessage(`{"path":"a"}`)}),
					sseContentBlockStop(1),
					sseMessageDelta("tool_use", &wireUsage{OutputTokens: int64Pointer(2)}),
					sseMessageStop(),
				)
			case 2:
				if len(wire.Messages) != 3 {
					t.Fatalf("second messages = %s", wire.Messages)
				}
				if !strings.Contains(string(wire.Messages[1]), `"signature":"signed"`) || !strings.Contains(string(wire.Messages[1]), `"input":{"path":"a"}`) {
					t.Fatalf("assistant replay = %s", wire.Messages[1])
				}
				var user wireMessage
				if err := json.Unmarshal(wire.Messages[2], &user); err != nil {
					t.Fatal(err)
				}
				var blocks []contentBlock
				if err := json.Unmarshal(user.Content, &blocks); err != nil {
					t.Fatal(err)
				}
				if len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "call_1" || blocks[0].Content != "contents" {
					t.Fatalf("tool result = %+v", blocks)
				}
				stream = marshalSSE(
					t,
					sseMessageStart(&wireUsage{InputTokens: int64Pointer(12), OutputTokens: int64Pointer(0)}),
					sseContentBlockStart(t, 0, contentBlock{Type: "text"}),
					sseContentBlockDelta(0, streamDelta{Type: "text_delta", Text: "done"}),
					sseContentBlockStop(0),
					sseMessageDelta("end_turn", &wireUsage{OutputTokens: int64Pointer(1)}),
					sseMessageStop(),
				)
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
		})},
		PrepareRequest: func(_ context.Context, request *http.Request) error {
			request.Header.Set("x-api-key", "secret")
			return nil
		},
		RequestOptions: func(agent.Request) (RequestOptions, error) {
			return RequestOptions{MaxTokens: 32_000, ToolChoice: &ToolChoice{Type: "auto"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := client.Generate(context.Background(), agent.Request{
		Model:        "model",
		Instructions: "instructions",
		Inputs:       []agent.Input{agent.NewTextInput("hello")},
		Tools:        []agent.ToolDefinition{{Name: "read", Parameters: agent.JSONSchema{Type: "object"}}},
	}, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || string(first.ToolCalls[0].Arguments) != `{"path":"a"}` || first.Usage.TotalTokens != 12 {
		t.Fatalf("first response = %+v", first)
	}

	second, err := client.Generate(context.Background(), agent.Request{
		Model:        "model",
		Instructions: "instructions",
		State:        first.State,
		Inputs:       []agent.Input{agent.NewToolResultInput(agent.ToolResult{CallID: "call_1", Tool: "read", Output: "contents"})},
	}, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "done" || requestNumber != 2 {
		t.Fatalf("second response = %+v, requests = %d", second, requestNumber)
	}
}

func TestSemanticCompactStoresValidAnthropicExchange(t *testing.T) {
	client, err := New(Options{
		Endpoint: "https://example.test/v1/messages",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"tools"`) || strings.Contains(string(body), `"tool_choice"`) {
				t.Fatalf("summary request contains tool settings: %s", body)
			}
			stream := marshalSSE(
				t,
				sseMessageStart(nil),
				sseContentBlockStart(t, 0, contentBlock{Type: "text", Text: "summary"}),
				sseContentBlockStop(0),
				sseMessageDelta("end_turn", nil),
				sseMessageStop(),
			)
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
		})},
		RequestOptions: func(agent.Request) (RequestOptions, error) {
			return RequestOptions{MaxTokens: 100, ToolChoice: &ToolChoice{Type: "auto"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	compacted, err := client.SemanticCompact(context.Background(), agent.Request{
		Model:  "model",
		Inputs: []agent.Input{agent.NewTextInput("pending")},
		Tools:  []agent.ToolDefinition{{Name: "read", Parameters: agent.JSONSchema{Type: "object"}}},
	}, "summarize")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := continuation.Decode(compacted.State, continuation.DefaultMaximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("compacted messages = %s", messages)
	}
	var summaryMessage wireMessage
	if err := json.Unmarshal(messages[1], &summaryMessage); err != nil {
		t.Fatal(err)
	}
	var summaryBlocks []contentBlock
	if err := json.Unmarshal(summaryMessage.Content, &summaryBlocks); err != nil {
		t.Fatal(err)
	}
	var continuationMessage wireMessage
	if err := json.Unmarshal(messages[2], &continuationMessage); err != nil {
		t.Fatal(err)
	}
	var continuationBlocks []contentBlock
	if err := json.Unmarshal(continuationMessage.Content, &continuationBlocks); err != nil {
		t.Fatal(err)
	}
	if summaryMessage.Role != "assistant" || len(summaryBlocks) != 1 || !strings.Contains(summaryBlocks[0].Text, "summary") || continuationMessage.Role != "user" || len(continuationBlocks) != 1 || continuationBlocks[0].Text == "" {
		t.Fatalf("compacted messages = %s", messages)
	}
}
