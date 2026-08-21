package app

import (
	"slices"
	"testing"
)

func TestBuildToolset(t *testing.T) {
	for _, test := range []struct {
		name   string
		access toolAccess
		want   []string
	}{
		{name: "full access", access: fullToolAccess, want: []string{"bash", "edit", "read", "write"}},
		{name: "read only", access: readOnlyToolAccess, want: []string{"read"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := buildToolset(t.TempDir(), test.access, true, nil)
			if err != nil {
				t.Fatal(err)
			}

			names := make([]string, len(registry.Definitions()))
			for index, definition := range registry.Definitions() {
				names[index] = definition.Name
			}
			if !slices.Equal(names, test.want) {
				t.Fatalf("tools = %v, want %v", names, test.want)
			}
		})
	}
}
