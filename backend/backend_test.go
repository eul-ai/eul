package backend

import (
	"errors"
	"testing"

	"github.com/eul-ai/eul/agent"
)

type testDriver struct {
	descriptor Descriptor
}

func (driver testDriver) Descriptor() Descriptor { return driver.descriptor }
func (testDriver) ModelDefaults() ModelDefaults {
	return ModelDefaults{}
}
func (testDriver) Open(Options) (Runtime, error) { return testRuntime{}, nil }

type testRuntime struct{}

func (testRuntime) NewProvider() (agent.Provider, error) {
	return nil, errors.New("unused")
}
func (testRuntime) Close() error { return nil }

func TestRegistrySelectsDefaultAndExplicitProviders(t *testing.T) {
	first := testDriver{descriptor: Descriptor{ID: "first", Name: "First"}}
	second := testDriver{descriptor: Descriptor{ID: "second", Name: "Second"}}
	registry, err := NewRegistry("first", first, second)
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range map[string]string{"": "first", "first": "first", "second": "second"} {
		driver, err := registry.Lookup(id)
		if err != nil {
			t.Fatalf("lookup %q: %v", id, err)
		}
		if driver.Descriptor().ID != want {
			t.Fatalf("lookup %q = %q, want %q", id, driver.Descriptor().ID, want)
		}
	}
	if _, err := registry.Lookup("missing"); err == nil {
		t.Fatal("missing provider was accepted")
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := testDriver{descriptor: Descriptor{ID: "valid", Name: "Valid"}}
	tests := []struct {
		name      string
		defaultID string
		drivers   []Driver
	}{
		{name: "invalid default", defaultID: "Invalid", drivers: []Driver{valid}},
		{name: "missing default", defaultID: "missing", drivers: []Driver{valid}},
		{name: "nil driver", defaultID: "valid", drivers: []Driver{valid, nil}},
		{name: "invalid ID", defaultID: "valid", drivers: []Driver{valid, testDriver{descriptor: Descriptor{ID: "Bad", Name: "Bad"}}}},
		{name: "empty name", defaultID: "valid", drivers: []Driver{valid, testDriver{descriptor: Descriptor{ID: "empty", Name: " "}}}},
		{name: "duplicate", defaultID: "valid", drivers: []Driver{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRegistry(test.defaultID, test.drivers...); err == nil {
				t.Fatal("invalid registry was accepted")
			}
		})
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"openai-codex", "provider.v2", "a_1"} {
		if !ValidID(id) {
			t.Fatalf("valid ID %q was rejected", id)
		}
	}
	for _, id := range []string{"", "-provider", "Provider", "provider name", "provider/child"} {
		if ValidID(id) {
			t.Fatalf("invalid ID %q was accepted", id)
		}
	}
}
