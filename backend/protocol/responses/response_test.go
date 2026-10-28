package responses

import (
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestNormalizeUsageDerivesMissingTotal(t *testing.T) {
	if usage := normalizeUsage(&responseUsage{InputTokens: 12, OutputTokens: 3}); usage != (agent.Usage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15}) {
		t.Fatalf("usage = %+v", usage)
	}
}
