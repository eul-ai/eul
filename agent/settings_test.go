package agent

import (
	"slices"
	"testing"
)

func TestParseThinkingLevel(t *testing.T) {
	for _, want := range ThinkingLevels() {
		got, err := ParseThinkingLevel(string(want))
		if err != nil || got != want {
			t.Fatalf("ParseThinkingLevel(%q) = %q, %v", want, got, err)
		}
	}
	for _, value := range []string{"", "none", "default", "HIGH", "extreme"} {
		if _, err := ParseThinkingLevel(value); err == nil {
			t.Fatalf("ParseThinkingLevel(%q) succeeded", value)
		}
	}
}

func TestThinkingLevelMapOrdersAndClampsLevels(t *testing.T) {
	levels := ThinkingLevelMap{
		ThinkingOff:    "disabled",
		ThinkingLow:    "low",
		ThinkingMedium: "medium",
		ThinkingHigh:   "high",
	}
	if got, want := levels.SupportedLevels(), []ThinkingLevel{ThinkingOff, ThinkingLow, ThinkingMedium, ThinkingHigh}; !slices.Equal(got, want) {
		t.Fatalf("SupportedLevels() = %v, want %v", got, want)
	}
	for requested, want := range map[ThinkingLevel]ThinkingLevel{
		ThinkingMinimal: ThinkingLow,
		ThinkingXHigh:   ThinkingHigh,
		ThinkingMax:     ThinkingHigh,
	} {
		if got := levels.Clamp(requested); got != want {
			t.Fatalf("Clamp(%q) = %q, want %q", requested, got, want)
		}
	}
}
