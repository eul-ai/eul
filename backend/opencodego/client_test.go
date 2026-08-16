package opencodego

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testModelInfos() map[string]modelInfo {
	return map[string]modelInfo{
		"grok-4.5": {
			protocol:              protocolResponses,
			contextWindow:         500_000,
			maxOutputTokens:       500_000,
			thinkingLevels:        []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh},
			thinkingMode:          thinkingEffort,
			thinkingEfforts:       effortValues(agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh),
			includeEncryptedState: true,
		},
		"gpt-5.6-luna": {
			protocol:              protocolResponses,
			contextWindow:         1_050_000,
			maxOutputTokens:       128_000,
			thinkingLevels:        []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh, agent.ThinkingXHigh, agent.ThinkingMax},
			thinkingMode:          thinkingEffort,
			thinkingEfforts:       effortValues(agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh, agent.ThinkingXHigh, agent.ThinkingMax),
			includeEncryptedState: true,
			lowTextVerbosity:      true,
		},
		"glm-5.2": {
			protocol:        protocolChatCompletions,
			contextWindow:   1_000_000,
			maxOutputTokens: 131_072,
			thinkingLevels:  []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
			thinkingMode:    thinkingEffort,
			thinkingEfforts: effortValues(agent.ThinkingHigh, agent.ThinkingMax),
		},
		"hy3": {
			protocol:        protocolChatCompletions,
			contextWindow:   256_000,
			maxOutputTokens: 64_000,
			thinkingLevels:  []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh},
			thinkingMode:    thinkingEffort,
			thinkingEfforts: map[agent.ThinkingLevel]string{
				agent.ThinkingOff:  "none",
				agent.ThinkingLow:  "low",
				agent.ThinkingHigh: "high",
			},
		},
		"deepseek-v4-pro": {
			protocol:                  protocolChatCompletions,
			contextWindow:             1_000_000,
			maxOutputTokens:           384_000,
			thinkingLevels:            []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
			thinkingMode:              thinkingEffort,
			thinkingEfforts:           effortValues(agent.ThinkingHigh, agent.ThinkingMax),
			serializeReasoningContent: true,
		},
		"minimax-m3": {
			protocol:        protocolAnthropicMessages,
			contextWindow:   1_000_000,
			maxOutputTokens: 131_072,
			thinkingLevels:  []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
			thinkingMode:    thinkingAdaptive,
		},
		"qwen3.8-max": {
			protocol:        protocolAnthropicMessages,
			contextWindow:   1_000_000,
			maxOutputTokens: 131_072,
			thinkingLevels:  []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
			thinkingMode:    thinkingBudget,
		},
	}
}

func effortValues(levels ...agent.ThinkingLevel) map[agent.ThinkingLevel]string {
	values := make(map[agent.ThinkingLevel]string, len(levels))
	for _, level := range levels {
		if level == agent.ThinkingOff {
			values[level] = "none"
		} else {
			values[level] = string(level)
		}
	}
	return values
}

func TestProviderRoutesProtocols(t *testing.T) {
	models := testModelInfos()
	expected := map[protocol]string{
		protocolResponses:         "grok-4.5",
		protocolChatCompletions:   "glm-5.2",
		protocolAnthropicMessages: "minimax-m3",
	}

	requests := 0
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		info := models[payload.Model]

		var stream string
		switch info.protocol {
		case protocolResponses:
			if request.URL.Path != "/zen/go/v1/responses" || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("x-api-key") != "" {
				t.Errorf("Responses request path=%q headers=%v", request.URL.Path, request.Header)
			}
			stream = strings.Join([]string{
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"ok"}]}}`,
				`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
			}, "\n\n") + "\n\n"
		case protocolChatCompletions:
			if request.URL.Path != "/zen/go/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("x-api-key") != "" {
				t.Errorf("Chat request path=%q headers=%v", request.URL.Path, request.Header)
			}
			stream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		case protocolAnthropicMessages:
			if request.URL.Path != "/zen/go/v1/messages" || request.Header.Get("x-api-key") != "secret" || request.Header.Get("anthropic-version") != "2023-06-01" || request.Header.Get("Authorization") != "" {
				t.Errorf("Anthropic request path=%q headers=%v", request.URL.Path, request.Header)
			}
			if !strings.Contains(string(body), `"cache_control":{"type":"ephemeral"}`) {
				t.Errorf("Anthropic request has no prompt cache control: %s", body)
			}
			stream = strings.Join([]string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":2,"output_tokens":0}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`data: {"type":"message_stop"}`,
			}, "\n\n") + "\n\n"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(stream)),
		}, nil
	})}

	client, err := newProvider("secret", "https://example.test/zen/go/v1", httpClient, models)
	if err != nil {
		t.Fatal(err)
	}
	for _, modelID := range expected {
		response, err := client.Generate(context.Background(), agent.Request{
			Model:         modelID,
			ThinkingLevel: models[modelID].thinkingLevels[0],
			Inputs:        []agent.Input{agent.NewTextInput("hello")},
		}, agent.StreamObserver{})
		if err != nil {
			t.Fatalf("Generate(%q): %v", modelID, err)
		}
		if response.Text != "ok" {
			t.Fatalf("Generate(%q) text = %q", modelID, response.Text)
		}
	}
	if requests != len(expected) {
		t.Fatalf("requests = %d, want %d", requests, len(expected))
	}
}

func TestProviderRejectsUnknownModelBeforeRequest(t *testing.T) {
	requests := 0
	client, err := newProvider("secret", "https://example.test/zen/go/v1", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})}, testModelInfos())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(context.Background(), agent.Request{Model: "unknown"}, agent.StreamObserver{}); err == nil {
		t.Fatal("unknown model was accepted")
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestOpenCodeRequestOptions(t *testing.T) {
	client := &provider{models: testModelInfos()}
	grok, err := client.responseRequestOptions(agent.Request{Model: "grok-4.5", ThinkingLevel: agent.ThinkingMedium})
	if err != nil {
		t.Fatal(err)
	}
	if grok.Reasoning == nil || grok.Reasoning.Effort != "medium" || grok.Reasoning.Summary != "auto" || !slices.Equal(grok.Include, []string{"reasoning.encrypted_content"}) || grok.TextVerbosity != "" {
		t.Fatalf("Grok options = %+v", grok)
	}
	gpt, err := client.responseRequestOptions(agent.Request{Model: "gpt-5.6-luna", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if gpt.Reasoning == nil || gpt.Reasoning.Effort != "none" || gpt.Reasoning.Summary != "" || gpt.TextVerbosity != "low" {
		t.Fatalf("GPT options = %+v", gpt)
	}
	chat, err := client.chatRequestOptions(agent.Request{Model: "glm-5.2", ThinkingLevel: agent.ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	if chat.ReasoningEffort != "max" || chat.MaxTokens != client.models["glm-5.2"].maxOutputTokens || chat.SerializeReasoningContent {
		t.Fatalf("Chat options = %+v", chat)
	}
	hy, err := client.chatRequestOptions(agent.Request{Model: "hy3", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if hy.ReasoningEffort != "none" {
		t.Fatalf("Hy options = %+v", hy)
	}
	deepseek, err := client.chatRequestOptions(agent.Request{Model: "deepseek-v4-pro", ThinkingLevel: agent.ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	if !deepseek.SerializeReasoningContent {
		t.Fatalf("DeepSeek options = %+v", deepseek)
	}
	minimax, err := client.anthropicRequestOptions(agent.Request{Model: "minimax-m3", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if minimax.Thinking == nil || minimax.Thinking.Type != "disabled" {
		t.Fatalf("MiniMax options = %+v", minimax)
	}
	qwen, err := client.anthropicRequestOptions(agent.Request{Model: "qwen3.8-max", ThinkingLevel: agent.ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	qwenOutputTokens := client.models["qwen3.8-max"].maxOutputTokens
	if qwen.MaxTokens != qwenOutputTokens || qwen.Thinking == nil || qwen.Thinking.Type != "enabled" || qwen.Thinking.BudgetTokens != qwenOutputTokens-maxThinkingOutputHeadroom || qwen.MaxTokens-qwen.Thinking.BudgetTokens != maxThinkingOutputHeadroom {
		t.Fatalf("Qwen options = %+v", qwen)
	}
}

func TestProviderSemanticCompactionReservesMaxThinkingOutput(t *testing.T) {
	var received struct {
		MaxTokens int `json:"max_tokens"`
		Thinking  struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"thinking"`
	}
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/zen/go/v1/messages" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		stream := strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":2,"output_tokens":0}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"summary"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
		}, "\n\n") + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(stream)),
		}, nil
	})}

	models := testModelInfos()
	client, err := newProvider("secret", "https://example.test/zen/go/v1", httpClient, models)
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := client.Compact(context.Background(), agent.Request{
		Model:         "qwen3.8-max",
		ThinkingLevel: agent.ThinkingMax,
		Inputs:        []agent.Input{agent.NewTextInput("pending work")},
	})
	if err != nil {
		t.Fatal(err)
	}
	maxOutputTokens := models["qwen3.8-max"].maxOutputTokens
	if len(compacted.State) == 0 || received.MaxTokens != maxOutputTokens || received.Thinking.Type != "enabled" || received.Thinking.BudgetTokens != maxOutputTokens-maxThinkingOutputHeadroom {
		t.Fatalf("compacted=%+v request=%+v", compacted, received)
	}
}

func TestModelMetadataMatchesRuntimeModels(t *testing.T) {
	models := testModelInfos()
	for model, info := range models {
		metadata := metadataFor(models, model)
		if metadata.ContextWindow != info.contextWindow || !slices.Equal(metadata.ThinkingLevels, info.thinkingLevels) {
			t.Fatalf("metadata for %q = %+v, model info = %+v", model, metadata, info)
		}
	}
	unknown := metadataFor(models, "unknown")
	if unknown.ContextWindow != 0 || !slices.Equal(unknown.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("unknown metadata = %+v", unknown)
	}
}
