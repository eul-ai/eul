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

func TestProviderRoutesEveryDocumentedModel(t *testing.T) {
	expected := map[protocol][]string{
		protocolResponses: {
			"grok-4.5",
			"gpt-5.6-luna",
		},
		protocolChatCompletions: {
			"glm-5.3",
			"glm-5.2",
			"glm-5.1",
			"kimi-k3",
			"kimi-k2.7-code",
			"kimi-k2.6",
			"deepseek-v4-pro",
			"deepseek-v4-flash",
			"mimo-v2.5",
			"mimo-v2.5-pro",
			"hy3",
		},
		protocolAnthropicMessages: {
			"minimax-m3",
			"minimax-m2.7",
			"minimax-m2.5",
			"qwen3.8-max",
			"qwen3.7-max",
			"qwen3.7-plus",
			"qwen3.6-plus",
		},
	}

	modelCount := 0
	for expectedProtocol, modelIDs := range expected {
		modelCount += len(modelIDs)
		for _, modelID := range modelIDs {
			info, ok := models[modelID]
			if !ok || info.protocol != expectedProtocol {
				t.Fatalf("model %q = %+v, present=%v", modelID, info, ok)
			}
		}
	}
	if len(models) != modelCount {
		t.Fatalf("model table contains %d models, want %d", len(models), modelCount)
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

	client, err := newProvider("secret", "https://example.test/zen/go/v1", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	for _, modelIDs := range expected {
		for _, modelID := range modelIDs {
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
	}
	if requests != modelCount {
		t.Fatalf("requests = %d, want %d", requests, modelCount)
	}
}

func TestProviderRejectsUnknownModelBeforeRequest(t *testing.T) {
	requests := 0
	client, err := newProvider("secret", "https://example.test/zen/go/v1", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})})
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
	grok, err := responseRequestOptions(agent.Request{Model: "grok-4.5", ThinkingLevel: agent.ThinkingMedium})
	if err != nil {
		t.Fatal(err)
	}
	if grok.Reasoning == nil || grok.Reasoning.Effort != "medium" || !slices.Equal(grok.Include, []string{"reasoning.encrypted_content"}) || grok.TextVerbosity != "" {
		t.Fatalf("Grok options = %+v", grok)
	}
	gpt, err := responseRequestOptions(agent.Request{Model: "gpt-5.6-luna", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if gpt.Reasoning == nil || gpt.Reasoning.Effort != "none" || gpt.TextVerbosity != "low" {
		t.Fatalf("GPT options = %+v", gpt)
	}
	chat, err := chatRequestOptions(agent.Request{Model: "glm-5.2", ThinkingLevel: agent.ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	if chat.ReasoningEffort != "max" || chat.MaxTokens != maxOutputTokens {
		t.Fatalf("Chat options = %+v", chat)
	}
	minimax, err := anthropicRequestOptions(agent.Request{Model: "minimax-m3", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if minimax.Thinking == nil || minimax.Thinking.Type != "disabled" {
		t.Fatalf("MiniMax options = %+v", minimax)
	}
	qwen, err := anthropicRequestOptions(agent.Request{Model: "qwen3.8-max", ThinkingLevel: agent.ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	if qwen.Thinking == nil || qwen.Thinking.Type != "enabled" || qwen.Thinking.BudgetTokens != maxOutputTokens-1 {
		t.Fatalf("Qwen options = %+v", qwen)
	}
}

func TestModelMetadataMatchesRoutingTable(t *testing.T) {
	for model, info := range models {
		metadata := metadataFor(model)
		if metadata.ContextWindow != info.contextWindow || !slices.Equal(metadata.ThinkingLevels, info.thinkingLevels) {
			t.Fatalf("metadata for %q = %+v, model info = %+v", model, metadata, info)
		}
	}
	unknown := metadataFor("unknown")
	if unknown.ContextWindow != 0 || !slices.Equal(unknown.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("unknown metadata = %+v", unknown)
	}
}
