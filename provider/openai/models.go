package openai

import "yaah/agent"

var modelContextWindows = map[string]int64{
	"gpt-5.6-luna":  272_000,
	"gpt-5.6-terra": 272_000,
	"gpt-5.6-sol":   272_000,
}

func (*Client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	if len(request.State) == 0 || usage.TotalTokens <= 0 {
		return false
	}

	contextWindow, ok := modelContextWindows[request.Model]
	if !ok {
		return false
	}
	limit := contextWindow * 9 / 10
	if usage.TotalTokens >= limit {
		return true
	}
	return estimateInputTokens(request.Inputs) >= limit-usage.TotalTokens
}

func estimateInputTokens(inputs []agent.Input) int64 {
	var total int64
	for _, input := range inputs {
		bytes := int64(len(input.Text))
		total += bytes / 4
		if bytes%4 != 0 {
			total++
		}
	}
	return total
}
