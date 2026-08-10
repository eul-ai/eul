package tool

import (
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestSubagentFinalizationPolicyDefaults(t *testing.T) {
	var reason agent.FinalizationReason
	policy := NewSubagentFinalizationPolicy(func(got agent.FinalizationReason) { reason = got })
	if policy.AfterDuration != 5*time.Minute || policy.AfterGenerations != 20 {
		t.Fatalf("policy = %+v", policy)
	}
	if !strings.Contains(policy.Prompt, "Do not call tools") || policy.OnBegin == nil {
		t.Fatalf("policy = %+v", policy)
	}
	policy.OnBegin(agent.FinalizationReasonGenerations)
	if reason != agent.FinalizationReasonGenerations {
		t.Fatalf("finalization reason = %q", reason)
	}
}
