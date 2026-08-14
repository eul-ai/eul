package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestClientUsesOpenRouterResponsesEndpointHeadersAndState(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/responses" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		for header, want := range map[string]string{
			"Authorization": "Bearer secret",
			"HTTP-Referer":  "https://github.com/eul-ai/eul",
			"X-Title":       "Eul",
			"User-Agent":    "eul",
		} {
			if got := request.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		if request.Header.Get("chatgpt-account-id") != "" || request.Header.Get("OpenAI-Beta") != "" || request.Header.Get("x-codex-beta-features") != "" {
			t.Errorf("Codex headers leaked: %v", request.Header)
		}

		var wire struct {
			Model             string            `json:"model"`
			Input             []json.RawMessage `json:"input"`
			Stream            bool              `json:"stream"`
			Reasoning         map[string]string `json:"reasoning"`
			Include           []string          `json:"include"`
			ServiceTier       string            `json:"service_tier"`
			ParallelToolCalls bool              `json:"parallel_tool_calls"`
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if wire.Model != "vendor/model" || !wire.Stream || wire.Reasoning["effort"] != "high" || len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" || wire.ServiceTier != "" || !wire.ParallelToolCalls {
			t.Errorf("request = %+v", wire)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			if len(wire.Input) != 1 {
				t.Errorf("first input count = %d", len(wire.Input))
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"format\":\"google-gemini-v1\",\"encrypted_content\":\"opaque-thought-signature\",\"summary\":[]}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_read\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"file.go\\\"}\"}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
		case 2:
			if len(wire.Input) != 4 || !strings.Contains(string(wire.Input[1]), `"encrypted_content":"opaque-thought-signature"`) || !strings.Contains(string(wire.Input[2]), `"call_id":"call_read"`) || !strings.Contains(string(wire.Input[3]), `"type":"function_call_output"`) || !strings.Contains(string(wire.Input[3]), "[tool error]") {
				t.Errorf("replayed input = %s", wire.Input)
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		}
	}))
	defer server.Close()

	client, err := newClient("secret", server.URL+"/responses", server.Client(), func(string) bool { return true }, func(string) int64 { return 128_000 })
	if err != nil {
		t.Fatal(err)
	}
	var reasoning string
	first, err := client.Generate(context.Background(), agent.Request{
		Model:         "vendor/model",
		ThinkingLevel: agent.ThinkingHigh,
		Inputs:        []agent.Input{agent.NewTextInput("inspect")},
	}, agent.StreamObserver{Reasoning: func(delta string) error { reasoning += delta; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_read" || reasoning != "thinking" || first.Usage.TotalTokens != 5 {
		t.Fatalf("first response = %+v reasoning=%q", first, reasoning)
	}

	second, err := client.Generate(context.Background(), agent.Request{
		Model:         "vendor/model",
		ThinkingLevel: agent.ThinkingHigh,
		State:         first.State,
		Inputs: []agent.Input{{
			Kind: agent.InputToolResult, CallID: "call_read", Tool: "read", Text: "timed out", IsError: true,
		}},
	}, agent.StreamObserver{})
	if err != nil || second.Text != "done" || calls != 2 {
		t.Fatalf("second response = %+v error=%v calls=%d", second, err, calls)
	}
}

func TestClientStreamsInterleavedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"one\",\"name\":\"read\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"two\",\"name\":\"read\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"path\\\":\\\"two\\\"}\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"path\\\":\\\"one\\\"}\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"one\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"one\\\"}\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"two\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"two\\\"}\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	provider, err := newClient("secret", server.URL, server.Client(), func(string) bool { return false }, func(string) int64 { return 128_000 })
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []agent.ToolCallSnapshot
	response, err := provider.Generate(context.Background(), agent.Request{
		Model: "vendor/plain", ThinkingLevel: agent.ThinkingOff,
	}, agent.StreamObserver{ToolCall: func(snapshot agent.ToolCallSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}})
	if err != nil || len(response.ToolCalls) != 2 || len(snapshots) != 6 || snapshots[2].ID != "two" || snapshots[3].ID != "one" || !snapshots[4].Complete || !snapshots[5].Complete {
		t.Fatalf("response=%+v snapshots=%+v error=%v", response, snapshots, err)
	}
}

func TestClientEncodesImageInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if len(wire.Input) != 1 || !strings.Contains(string(wire.Input[0]), `"type":"input_image"`) || !strings.Contains(string(wire.Input[0]), `"image_url":"data:image/png;base64,cG5n"`) {
			t.Errorf("input = %s", wire.Input)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	provider, err := newClient("secret", server.URL, server.Client(), func(string) bool { return false }, func(string) int64 { return 128_000 })
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Generate(context.Background(), agent.Request{
		Model:         "vendor/plain",
		ThinkingLevel: agent.ThinkingOff,
		Inputs: []agent.Input{{
			Kind: agent.InputUser,
			Content: []agent.ContentPart{{
				Kind:  agent.ContentPartImage,
				Image: &agent.Image{MediaType: "image/png", Data: []byte("png")},
			}},
		}},
	}, agent.StreamObserver{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientHandlesNumericRateLimitErrorAndRedactsKey(t *testing.T) {
	const key = "secret-openrouter-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: {\"type\":\"error\",\"error\":{\"code\":429,\"message\":\"rate limited %s\"}}\n\n", key)
	}))
	defer server.Close()

	provider, err := newClient(key, server.URL, server.Client(), func(string) bool { return false }, func(string) int64 { return 128_000 })
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Generate(context.Background(), agent.Request{
		Model: "vendor/plain", ThinkingLevel: agent.ThinkingOff,
	}, agent.StreamObserver{})
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), key) {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, retry := provider.RetryGeneration(err, 1); !retry {
		t.Fatal("numeric 429 stream error was not retryable")
	}
	if provider.ShouldCompactAfterError(agent.Request{}, err) {
		t.Fatal("rate limit error triggered compaction")
	}
}

func TestClientClassifiesContextLimitErrorForCompaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"too long\"}}\n\n")
	}))
	defer server.Close()

	client, err := newClient("secret", server.URL, server.Client(), func(string) bool { return false }, func(string) int64 { return 128_000 })
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), agent.Request{
		Model: "vendor/model", ThinkingLevel: agent.ThinkingOff,
	}, agent.StreamObserver{})
	if err == nil || !client.ShouldCompactAfterError(agent.Request{}, err) {
		t.Fatalf("Generate() error = %v, should compact = %t", err, client.ShouldCompactAfterError(agent.Request{}, err))
	}
}

func TestRequestOptionsRespectModelReasoningMetadata(t *testing.T) {
	options := requestOptions(func(model string) bool { return model == "vendor/reasoning" })

	_, err := options(agent.Request{Model: "vendor/reasoning", ThinkingLevel: agent.ThinkingMax})
	var unsupported *unsupportedThinkingLevelError
	if !errors.As(err, &unsupported) || unsupported.level != agent.ThinkingMax || unsupported.model != "vendor/reasoning" {
		t.Fatalf("max thinking error = %v", err)
	}
	_, err = options(agent.Request{Model: "vendor/plain", ThinkingLevel: agent.ThinkingMedium})
	unsupported = nil
	if !errors.As(err, &unsupported) || unsupported.level != agent.ThinkingMedium || unsupported.model != "vendor/plain" {
		t.Fatalf("plain model thinking error = %v", err)
	}
	off, err := options(agent.Request{Model: "vendor/plain", ThinkingLevel: agent.ThinkingOff})
	if err != nil || off.Reasoning != nil {
		t.Fatalf("plain model off options = %+v, %v", off, err)
	}
}

func TestClientShouldCompactAtModelContextThreshold(t *testing.T) {
	client, err := newClient(
		"secret",
		"http://127.0.0.1:1/responses",
		&http.Client{},
		func(string) bool { return false },
		func(model string) int64 {
			if model == "vendor/model" {
				return 1_000
			}
			return 0
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	state := []byte(`{"version":1,"items":[{"type":"message","role":"assistant","content":"history"}]}`)
	tests := []struct {
		name    string
		request agent.Request
		usage   agent.Usage
		want    bool
	}{
		{name: "no state", request: agent.Request{Model: "vendor/model"}, usage: agent.Usage{TotalTokens: 900}},
		{name: "no usage", request: agent.Request{Model: "vendor/model", State: state}},
		{name: "below threshold", request: agent.Request{Model: "vendor/model", State: state}, usage: agent.Usage{TotalTokens: 899}},
		{name: "at threshold", request: agent.Request{Model: "vendor/model", State: state}, usage: agent.Usage{TotalTokens: 900}, want: true},
		{name: "pending input crosses threshold", request: agent.Request{Model: "vendor/model", State: state, Inputs: []agent.Input{agent.NewTextInput("12345678")}}, usage: agent.Usage{TotalTokens: 898}, want: true},
		{name: "unknown model", request: agent.Request{Model: "vendor/unknown", State: state}, usage: agent.Usage{TotalTokens: 900}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := client.ShouldCompact(test.request, test.usage); got != test.want {
				t.Fatalf("ShouldCompact() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestClientSemanticallyCompactsAndReplaysSummary(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("HTTP-Referer") == "" {
			t.Errorf("headers = %v", request.Header)
		}

		var wire struct {
			Instructions      string            `json:"instructions"`
			Input             []json.RawMessage `json:"input"`
			Tools             []json.RawMessage `json:"tools"`
			ToolChoice        string            `json:"tool_choice"`
			ParallelToolCalls bool              `json:"parallel_tool_calls"`
			Reasoning         map[string]string `json:"reasoning"`
			Include           []string          `json:"include"`
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" {
			t.Errorf("include = %v", wire.Include)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			input, _ := json.Marshal(wire.Input)
			joined := string(input)
			if wire.Instructions == "original" || wire.Instructions == "" || len(wire.Input) != 2 || !strings.Contains(joined, "old answer") {
				t.Errorf("summary input = %s, instructions = %q", wire.Input, wire.Instructions)
			}
			if len(wire.Tools) != 0 || wire.ToolChoice != "" || wire.ParallelToolCalls || wire.Reasoning["effort"] != "high" || strings.Contains(joined, "compaction_trigger") {
				t.Errorf("summary request = %+v", wire)
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"concise handoff\"}]}],\"usage\":{\"input_tokens\":80,\"output_tokens\":10,\"total_tokens\":90}}}\n\n")
		case 2:
			input, _ := json.Marshal(wire.Input)
			joined := string(input)
			if len(wire.Input) != 2 {
				t.Errorf("continued input = %s", wire.Input)
			} else {
				var summaryMessage, userMessage struct {
					Role string `json:"role"`
				}
				_ = json.Unmarshal(wire.Input[0], &summaryMessage)
				_ = json.Unmarshal(wire.Input[1], &userMessage)
				if summaryMessage.Role != "assistant" || userMessage.Role != "user" || !strings.Contains(joined, "concise handoff") || !strings.Contains(joined, "continue") || strings.Contains(joined, "old answer") {
					t.Errorf("continued input = %s", wire.Input)
				}
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"continued\"}]}]}}\n\n")
		default:
			t.Errorf("unexpected request %d", calls)
		}
	}))
	defer server.Close()

	client, err := newClient("secret", server.URL, server.Client(), func(string) bool { return true }, func(string) int64 { return 128_000 })
	if err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"version":1,"items":[{"type":"message","role":"assistant","content":"old answer"}]}`)
	compacted, err := client.Compact(context.Background(), agent.Request{
		Model:         "vendor/model",
		ThinkingLevel: agent.ThinkingHigh,
		Instructions:  "original",
		State:         state,
		Tools:         []agent.ToolDefinition{{Name: "read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compacted.Usage != (agent.Usage{InputTokens: 80, OutputTokens: 10, TotalTokens: 90}) {
		t.Fatalf("compaction usage = %+v", compacted.Usage)
	}

	response, err := client.Generate(context.Background(), agent.Request{
		Model:         "vendor/model",
		ThinkingLevel: agent.ThinkingOff,
		State:         compacted.State,
		Inputs:        []agent.Input{agent.NewTextInput("continue")},
	}, agent.StreamObserver{})
	if err != nil || response.Text != "continued" || calls != 2 {
		t.Fatalf("response = %+v, error = %v, calls = %d", response, err, calls)
	}
}
