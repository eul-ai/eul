package builtins

import "testing"

func TestRegistryContainsBuiltInBackends(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range map[string]string{
		"":             "openai-codex",
		"openai-codex": "openai-codex",
		"opencode-go":  "opencode-go",
		"openrouter":   "openrouter",
	} {
		driver, err := registry.Lookup(id)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", id, err)
		}
		if driver.Descriptor().ID != want {
			t.Fatalf("Lookup(%q) = %q, want %q", id, driver.Descriptor().ID, want)
		}
	}
}
