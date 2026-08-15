package chatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientContinuesAssistantToolCalls(t *testing.T) {
	requestNumber := 0
	client, err := New(Options{
		Endpoint: "https://example.test/v1/chat/completions",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requestNumber++
			if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("headers = %v", request.Header)
			}
			var wire createRequest
			if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			if !wire.Stream || wire.StreamOptions == nil || !wire.StreamOptions.IncludeUsage {
				t.Fatalf("stream options = %+v", wire)
			}

			var stream string
			switch requestNumber {
			case 1:
				if len(wire.Messages) != 2 {
					t.Fatalf("first messages = %s", wire.Messages)
				}
				stream = strings.Join([]string{
					`data: {"choices":[{"index":0,"delta":{"reasoning_content":"check "},"finish_reason":null}]}`,
					`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}`,
					`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
					`data: [DONE]`,
				}, "\n\n") + "\n\n"
			case 2:
				if len(wire.Messages) != 4 {
					t.Fatalf("second messages = %s", wire.Messages)
				}
				var assistant assistantMessage
				if err := json.Unmarshal(wire.Messages[2], &assistant); err != nil {
					t.Fatal(err)
				}
				if assistant.ReasoningContent != "check " || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
					t.Fatalf("replayed assistant = %+v", assistant)
				}
				var tool message
				if err := json.Unmarshal(wire.Messages[3], &tool); err != nil {
					t.Fatal(err)
				}
				if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Content != "contents" {
					t.Fatalf("tool message = %+v", tool)
				}
				stream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
		})},
		PrepareRequest: func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer secret")
			return nil
		},
		RequestOptions: func(agent.Request) (RequestOptions, error) {
			return RequestOptions{MaxTokens: 32_000, ToolChoice: "auto", ParallelToolCalls: true}, nil
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
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_1" || first.Usage.TotalTokens != 12 || len(first.State) == 0 {
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

func TestSemanticCompactDisablesToolsAndStoresContinuation(t *testing.T) {
	client, err := New(Options{
		Endpoint: "https://example.test/v1/chat/completions",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"tools"`) || strings.Contains(string(body), `"tool_choice"`) || strings.Contains(string(body), `"parallel_tool_calls"`) {
				t.Fatalf("summary request contains tool settings: %s", body)
			}
			stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"summary\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
		})},
		RequestOptions: func(agent.Request) (RequestOptions, error) {
			return RequestOptions{MaxTokens: 100, ToolChoice: "auto", ParallelToolCalls: true}, nil
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
	messages, err := decodeState(compacted.State, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("compacted messages = %s", messages)
	}
	var summary assistantMessage
	if err := json.Unmarshal(messages[0], &summary); err != nil {
		t.Fatal(err)
	}
	var continuation message
	if err := json.Unmarshal(messages[1], &continuation); err != nil {
		t.Fatal(err)
	}
	summaryText, summaryOK := summary.Content.(string)
	continuationText, continuationOK := continuation.Content.(string)
	if summary.Role != "assistant" || !summaryOK || !strings.Contains(summaryText, "summary") || continuation.Role != "user" || !continuationOK || continuationText == "" {
		t.Fatalf("compacted messages = %s", messages)
	}
}
