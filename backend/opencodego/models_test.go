package opencodego

import (
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestBuildModelsUsesLiveCatalogIntersectionAndRichMetadata(t *testing.T) {
	catalog := testCatalogProvider(t)
	live := map[string]struct{}{
		"grok-4.5":        {},
		"gpt-5.6-luna":    {},
		"hy3":             {},
		"deepseek-v4-pro": {},
		"kimi-k3":         {},
		"kimi-k2.6":       {},
		"minimax-m3":      {},
		"qwen3.8-max":     {},
		"live-only":       {},
	}
	models := buildModels(catalog, live)
	if len(models) != len(live)-1 {
		t.Fatalf("models = %#v", models)
	}
	if _, ok := models["public-only"]; ok {
		t.Fatal("public-only model was included")
	}
	if _, ok := models["live-only"]; ok {
		t.Fatal("live-only model was included")
	}

	grok := models["grok-4.5"]
	if grok.protocol != protocolResponses || grok.contextWindow != 500_000 || grok.maxOutputTokens != 500_000 || !grok.includeEncryptedState || !slices.Equal(grok.thinkingLevels, []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) {
		t.Fatalf("Grok = %+v", grok)
	}
	gpt := models["gpt-5.6-luna"]
	if !gpt.lowTextVerbosity || gpt.thinkingEfforts[agent.ThinkingOff] != "none" || !slices.Contains(gpt.thinkingLevels, agent.ThinkingMax) {
		t.Fatalf("GPT = %+v", gpt)
	}
	hy := models["hy3"]
	if hy.protocol != protocolChatCompletions || hy.thinkingMode != thinkingEffort || hy.thinkingEfforts[agent.ThinkingOff] != "none" {
		t.Fatalf("Hy = %+v", hy)
	}
	deepseek := models["deepseek-v4-pro"]
	if deepseek.protocol != protocolChatCompletions || !deepseek.serializeReasoningContent {
		t.Fatalf("DeepSeek = %+v", deepseek)
	}
	kimi := models["kimi-k3"]
	if kimi.thinkingMode != thinkingEffort || !slices.Equal(kimi.thinkingLevels, []agent.ThinkingLevel{agent.ThinkingMax}) || kimi.thinkingEfforts[agent.ThinkingMax] != "max" {
		t.Fatalf("Kimi K3 = %+v", kimi)
	}
	fixedKimi := models["kimi-k2.6"]
	if fixedKimi.thinkingMode != thinkingFixed || !slices.Equal(fixedKimi.thinkingLevels, []agent.ThinkingLevel{agent.ThinkingHigh}) {
		t.Fatalf("Kimi K2.6 = %+v", fixedKimi)
	}
	minimax := models["minimax-m3"]
	if minimax.protocol != protocolAnthropicMessages || minimax.thinkingMode != thinkingAdaptive || !slices.Equal(minimax.thinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh}) {
		t.Fatalf("MiniMax = %+v", minimax)
	}
	qwen := models["qwen3.8-max"]
	if qwen.protocol != protocolAnthropicMessages || qwen.thinkingMode != thinkingBudget || !slices.Equal(qwen.thinkingLevels, []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax}) {
		t.Fatalf("Qwen = %+v", qwen)
	}
}

func TestBuildModelsSkipsCatalogControlsEulCannotRoute(t *testing.T) {
	catalog := testCatalogProvider(t)
	catalog.Models["missing-options"] = catalogModel{
		ID:        "missing-options",
		Reasoning: true,
		Limit:     catalogLimit{Context: 100_000, Output: 32_000},
	}
	toggleOptions := []catalogReasoningOption{{Type: "toggle"}}
	catalog.Models["chat-toggle"] = catalogModel{
		ID:               "chat-toggle",
		Reasoning:        true,
		ReasoningOptions: &toggleOptions,
		Limit:            catalogLimit{Context: 100_000, Output: 32_000},
	}
	effort := "high"
	effortOptions := []catalogReasoningOption{{Type: "effort", Values: []*string{&effort}}}
	catalog.Models["anthropic-effort"] = catalogModel{
		ID:               "anthropic-effort",
		Reasoning:        true,
		ReasoningOptions: &effortOptions,
		Limit:            catalogLimit{Context: 100_000, Output: 32_000},
		Provider:         catalogModelProvider{NPM: "@ai-sdk/anthropic"},
	}
	fixedOptions := []catalogReasoningOption{}
	catalog.Models["unknown-sdk"] = catalogModel{
		ID:               "unknown-sdk",
		Reasoning:        false,
		ReasoningOptions: &fixedOptions,
		Limit:            catalogLimit{Context: 100_000, Output: 32_000},
		Provider:         catalogModelProvider{NPM: "@ai-sdk/unknown"},
	}

	live := map[string]struct{}{
		"missing-options":  {},
		"chat-toggle":      {},
		"anthropic-effort": {},
		"unknown-sdk":      {},
	}
	if models := buildModels(catalog, live); len(models) != 0 {
		t.Fatalf("unsupported models = %#v", models)
	}
}
