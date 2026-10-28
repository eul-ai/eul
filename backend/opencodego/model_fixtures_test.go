package opencodego

import "testing"

func testLiveModelIDs() map[string]struct{} {
	return map[string]struct{}{
		"grok-4.5":        {},
		"gpt-5.6-luna":    {},
		"glm-5.2":         {},
		"hy3":             {},
		"deepseek-v4-pro": {},
		"kimi-k3":         {},
		"kimi-k2.6":       {},
		"minimax-m3":      {},
		"qwen3.8-max":     {},
	}
}

func testModelInfos(t *testing.T) map[string]modelInfo {
	t.Helper()
	return buildModels(testCatalogProvider(t), testLiveModelIDs())
}
