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
		{name: "subagent access", access: subagentToolAccess, want: []string{"bash", "read"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := buildToolset(t.TempDir(), test.access, true, nil)
			names := make([]string, len(tools))
			for index, current := range tools {
				names[index] = current.Definition().Name
			}
			slices.Sort(names)
			if !slices.Equal(names, test.want) {
				t.Fatalf("tools = %v, want %v", names, test.want)
			}
		})
	}
}
