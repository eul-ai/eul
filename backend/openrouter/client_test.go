package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/testhttp"
)

func testMetadata(reasoning bool, contextWindow int64) func(string) modelMetadata {
	return func(string) modelMetadata {
		metadata := modelMetadata{
			contextWindow:        contextWindow,
			thinkingLevels:       []agent.ThinkingLevel{agent.ThinkingOff},
			defaultThinkingLevel: agent.ThinkingOff,
		}
		if reasoning {
			metadata.reasoning = true
			metadata.thinkingLevels = []agent.ThinkingLevel{
				agent.ThinkingOff,
				agent.ThinkingMinimal,
				agent.ThinkingLow,
				agent.ThinkingMedium,
				agent.ThinkingHigh,
				agent.ThinkingXHigh,
			}
			metadata.defaultThinkingLevel = agent.ThinkingMedium
		}
		return metadata
	}
}

func TestClientConfiguresOpenRouterResponsesAdapter(t *testing.T) {
	calls := 0
	server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
			SessionID         string            `json:"session_id"`
			Model             string            `json:"model"`
			Stream            bool              `json:"stream"`
			Reasoning         map[string]string `json:"reasoning"`
			Include           []string          `json:"include"`
			ServiceTier       string            `json:"service_tier"`
			ToolChoice        string            `json:"tool_choice"`
			ParallelToolCalls bool              `json:"parallel_tool_calls"`
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if wire.SessionID != "session-123" || wire.Model != "vendor/model" || !wire.Stream || wire.Reasoning["effort"] != "high" || len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" || wire.ServiceTier != "" || wire.ToolChoice != "auto" || !wire.ParallelToolCalls {
			t.Errorf("request = %+v", wire)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()

	client, err := newClient("secret", server.URL+"/responses", server.Client(), testMetadata(true, 128_000))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), agent.Request{
		SessionID:     "session-123",
		Model:         "vendor/model",
		ThinkingLevel: agent.ThinkingHigh,
	}, agent.StreamObserver{})
	if err != nil || calls != 1 {
		t.Fatalf("Generate() error = %v, calls = %d", err, calls)
	}
}

func TestRequestOptionsRespectModelReasoningMetadata(t *testing.T) {
	options := requestOptions(func(model string) modelMetadata {
		if model == "vendor/reasoning" {
			return testMetadata(true, 0)(model)
		}
		return testMetadata(false, 0)(model)
	})

	defaults, err := options(agent.Request{Model: "vendor/reasoning"})
	if err != nil || defaults.Reasoning == nil || defaults.Reasoning.Effort != "medium" {
		t.Fatalf("default reasoning options = %+v, %v", defaults, err)
	}
	_, err = options(agent.Request{Model: "vendor/reasoning", ThinkingLevel: agent.ThinkingMax})
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
		func(model string) modelMetadata {
			if model == "vendor/model" {
				return modelMetadata{contextWindow: 1_000, thinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
			}
			return modelMetadata{}
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
