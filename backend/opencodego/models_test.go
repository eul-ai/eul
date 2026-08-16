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
	grokConfig, ok := grok.protocol.(responsesConfig)
	if !ok || grok.contextWindow != 500_000 || grokConfig.maxOutputTokens != 500_000 || !slices.Equal(grok.thinking.levels, []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) {
		t.Fatalf("Grok = %+v", grok)
	}
	gpt := models["gpt-5.6-luna"]
	gptConfig, ok := gpt.protocol.(responsesConfig)
	if !ok || !gptConfig.lowTextVerbosity || gpt.thinking.efforts[agent.ThinkingOff] != "none" || !slices.Contains(gpt.thinking.levels, agent.ThinkingMax) {
		t.Fatalf("GPT = %+v", gpt)
	}
	hy := models["hy3"]
	if _, ok := hy.protocol.(chatCompletionsConfig); !ok || hy.thinking.mode != thinkingEffort || hy.thinking.efforts[agent.ThinkingOff] != "none" {
		t.Fatalf("Hy = %+v", hy)
	}
	deepseek := models["deepseek-v4-pro"]
	deepseekConfig, ok := deepseek.protocol.(chatCompletionsConfig)
	if !ok || !deepseekConfig.serializeReasoningContent {
		t.Fatalf("DeepSeek = %+v", deepseek)
	}
	kimi := models["kimi-k3"]
	if kimi.thinking.mode != thinkingEffort || !slices.Equal(kimi.thinking.levels, []agent.ThinkingLevel{agent.ThinkingMax}) || kimi.thinking.efforts[agent.ThinkingMax] != "max" {
		t.Fatalf("Kimi K3 = %+v", kimi)
	}
	fixedKimi := models["kimi-k2.6"]
	if fixedKimi.thinking.mode != thinkingFixed || !slices.Equal(fixedKimi.thinking.levels, []agent.ThinkingLevel{agent.ThinkingHigh}) {
		t.Fatalf("Kimi K2.6 = %+v", fixedKimi)
	}
	minimax := models["minimax-m3"]
	if _, ok := minimax.protocol.(anthropicMessagesConfig); !ok || minimax.thinking.mode != thinkingAdaptive || !slices.Equal(minimax.thinking.levels, []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh}) {
		t.Fatalf("MiniMax = %+v", minimax)
	}
	qwen := models["qwen3.8-max"]
	qwenConfig, ok := qwen.protocol.(anthropicMessagesConfig)
	if !ok || qwen.thinking.mode != thinkingBudget || qwen.thinking.maxBudgetTokens != qwenConfig.maxOutputTokens-maxThinkingOutputHeadroom || !slices.Equal(qwen.thinking.levels, []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax}) {
		t.Fatalf("Qwen = %+v", qwen)
	}
}

func TestBuildModelInfoHonorsThinkingBudgetMaximum(t *testing.T) {
	options := []catalogReasoningOption{{Type: reasoningOptionTypeBudgetTokens, Max: 24_000}}
	info, ok := buildModelInfo("@ai-sdk/anthropic", "budget-model", catalogModel{
		ID:               "budget-model",
		Reasoning:        true,
		ReasoningOptions: &options,
		Limit:            catalogLimit{Context: 100_000, Output: 64_000},
	})
	if !ok || info.thinking.maxBudgetTokens != 24_000 {
		t.Fatalf("model info = %+v, ok = %t", info, ok)
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
	for id, model := range map[string]catalogModel{
		"missing-budget-maximum": {
			Limit: catalogLimit{Context: 100_000, Output: 32_000},
		},
		"low-budget-maximum": {
			Limit: catalogLimit{Context: 100_000, Output: 32_000},
		},
		"small-budget-output": {
			Limit: catalogLimit{Context: 100_000, Output: 24_000},
		},
	} {
		model.ID = id
		model.Reasoning = true
		model.Provider.NPM = "@ai-sdk/anthropic"
		maximum := 0
		switch id {
		case "low-budget-maximum":
			maximum = highThinkingBudgetTokens
		case "small-budget-output":
			maximum = 100_000
		}
		options := []catalogReasoningOption{{Type: reasoningOptionTypeBudgetTokens, Max: maximum}}
		model.ReasoningOptions = &options
		catalog.Models[id] = model
	}

	live := map[string]struct{}{
		"missing-options":        {},
		"chat-toggle":            {},
		"anthropic-effort":       {},
		"unknown-sdk":            {},
		"missing-budget-maximum": {},
		"low-budget-maximum":     {},
		"small-budget-output":    {},
	}
	if models := buildModels(catalog, live); len(models) != 0 {
		t.Fatalf("unsupported models = %#v", models)
	}
}
