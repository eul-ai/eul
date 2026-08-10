package tool

import (
	"strings"
	"testing"
	"time"
)

func TestSubagentFinalizationPolicyDefaults(t *testing.T) {
	began := false
	policy := NewSubagentFinalizationPolicy(func() { began = true })
	if policy.AfterDuration != 5*time.Minute || policy.AfterTokens != 200_000 || policy.AfterGenerations != 20 {
		t.Fatalf("policy = %+v", policy)
	}
	if !strings.Contains(policy.Prompt, "Do not call tools") || policy.OnBegin == nil {
		t.Fatalf("policy = %+v", policy)
	}
	policy.OnBegin()
	if !began {
		t.Fatal("finalization callback was not retained")
	}
}
