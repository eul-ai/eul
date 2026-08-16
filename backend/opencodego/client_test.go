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

func TestProviderRoutesProtocols(t *testing.T) {
	models := testModelInfos(t)
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
			Model     string `json:"model"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		info := models[payload.Model]

		var stream string
		switch info.protocol {
		case protocolResponses:
			if request.URL.Path != "/zen/go/v1/responses" || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("x-api-key") != "" || payload.SessionID != "session-id" {
				t.Errorf("Responses request path=%q session=%q headers=%v", request.URL.Path, payload.SessionID, request.Header)
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
			SessionID:     "session-id",
			Model:         modelID,
			ThinkingLevel: models[modelID].thinking.levels[0],
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

func TestProviderCompactionRequiresState(t *testing.T) {
	client, err := newProvider("secret", "https://example.test/zen/go/v1", &http.Client{}, testModelInfos(t))
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{Model: "grok-4.5", ThinkingLevel: agent.ThinkingMedium}
	usage := agent.Usage{TotalTokens: 900_000}
	if client.ShouldCompact(request, usage) {
		t.Fatal("request without state triggered compaction")
	}
	request.State = []byte(`{"version":1,"items":[]}`)
	if !client.ShouldCompact(request, usage) {
		t.Fatal("request at context threshold did not trigger compaction")
	}
}

func TestProviderRejectsUnknownModelBeforeRequest(t *testing.T) {
	requests := 0
	client, err := newProvider("secret", "https://example.test/zen/go/v1", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})}, testModelInfos(t))
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
	client := &provider{models: testModelInfos(t)}
	grok, err := responseRequestOptions(client.models, agent.Request{SessionID: "session-id", Model: "grok-4.5", ThinkingLevel: agent.ThinkingMedium})
	if err != nil {
		t.Fatal(err)
	}
	if grok.SessionID != "session-id" || grok.Reasoning == nil || grok.Reasoning.Effort != "medium" || grok.Reasoning.Summary != "auto" || !slices.Equal(grok.Include, []string{"reasoning.encrypted_content"}) || grok.TextVerbosity != "" {
		t.Fatalf("Grok options = %+v", grok)
	}
	gpt, err := responseRequestOptions(client.models, agent.Request{Model: "gpt-5.6-luna", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if gpt.Reasoning == nil || gpt.Reasoning.Effort != "none" || gpt.Reasoning.Summary != "" || gpt.TextVerbosity != "low" {
		t.Fatalf("GPT options = %+v", gpt)
	}
	chat, err := chatRequestOptions(client.models, agent.Request{Model: "glm-5.2", ThinkingLevel: agent.ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	if chat.ReasoningEffort != "max" || chat.MaxTokens != client.models["glm-5.2"].maxOutputTokens || chat.SerializeReasoningContent {
		t.Fatalf("Chat options = %+v", chat)
	}
	hy, err := chatRequestOptions(client.models, agent.Request{Model: "hy3", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if hy.ReasoningEffort != "none" {
		t.Fatalf("Hy options = %+v", hy)
	}
	deepseek, err := chatRequestOptions(client.models, agent.Request{Model: "deepseek-v4-pro", ThinkingLevel: agent.ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	if !deepseek.SerializeReasoningContent {
		t.Fatalf("DeepSeek options = %+v", deepseek)
	}
	minimax, err := anthropicRequestOptions(client.models, agent.Request{Model: "minimax-m3", ThinkingLevel: agent.ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	if minimax.Thinking == nil || minimax.Thinking.Type != "disabled" {
		t.Fatalf("MiniMax options = %+v", minimax)
	}
	qwenHigh, err := anthropicRequestOptions(client.models, agent.Request{Model: "qwen3.8-max", ThinkingLevel: agent.ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	if qwenHigh.Thinking == nil || qwenHigh.Thinking.BudgetTokens != highThinkingBudgetTokens {
		t.Fatalf("Qwen high options = %+v", qwenHigh)
	}
	qwenMax, err := anthropicRequestOptions(client.models, agent.Request{Model: "qwen3.8-max", ThinkingLevel: agent.ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	qwenInfo := client.models["qwen3.8-max"]
	if qwenMax.MaxTokens != qwenInfo.maxOutputTokens || qwenMax.Thinking == nil || qwenMax.Thinking.Type != "enabled" || qwenMax.Thinking.BudgetTokens != qwenInfo.thinking.maxBudgetTokens || qwenMax.MaxTokens-qwenMax.Thinking.BudgetTokens < maxThinkingOutputHeadroom {
		t.Fatalf("Qwen max options = %+v", qwenMax)
	}
}

func TestProviderSemanticCompactionUsesMaxThinkingBudget(t *testing.T) {
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

	models := testModelInfos(t)
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
	info := models["qwen3.8-max"]
	if len(compacted.State) == 0 || received.MaxTokens != info.maxOutputTokens || received.Thinking.Type != "enabled" || received.Thinking.BudgetTokens != info.thinking.maxBudgetTokens {
		t.Fatalf("compacted=%+v request=%+v", compacted, received)
	}
}

func TestModelMetadataMatchesRuntimeModels(t *testing.T) {
	models := testModelInfos(t)
	for model, info := range models {
		metadata := metadataFor(models, model)
		if metadata.ContextWindow != info.contextWindow || !slices.Equal(metadata.ThinkingLevels, info.thinking.levels) {
			t.Fatalf("metadata for %q = %+v, model info = %+v", model, metadata, info)
		}
	}
	unknown := metadataFor(models, "unknown")
	if unknown.ContextWindow != 0 || !slices.Equal(unknown.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("unknown metadata = %+v", unknown)
	}
}
