package tool

import (
	"encoding/json"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestSpecPresentsPartialArgumentsLeniently(t *testing.T) {
	type arguments struct {
		Path    string `json:"path"`
		Count   int    `json:"count"`
		Content string `json:"content"`
	}
	var presented arguments
	tool := spec[arguments]{
		definition: agent.ToolDefinition{Name: "write"},
		present: func(args arguments) agent.ToolPresentation {
			presented = args
			return agent.ToolPresentation{}
		},
	}

	tool.Presentation(`{"path":"demo.go","count":"many","content":"partial`)
	if presented != (arguments{Path: "demo.go", Content: "partial"}) {
		t.Fatalf("arguments = %+v", presented)
	}
}

func TestDecodeArguments(t *testing.T) {
	type arguments struct {
		Path  string `json:"path"`
		Limit *int   `json:"limit"`
	}

	limit := 10
	for _, test := range []struct {
		name    string
		input   string
		want    arguments
		wantErr bool
	}{
		{name: "valid", input: `{"path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "case insensitive", input: `{"PATH":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "duplicate uses last", input: `{"path":"first","path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "unknown", input: `{"path":"README.md","extra":true}`, wantErr: true},
		{name: "non object", input: `[]`, wantErr: true},
		{name: "malformed", input: `{"path":`, wantErr: true},
		{name: "trailing", input: `{"path":"README.md"} {}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeArguments[arguments](json.RawMessage(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatal("decodeArguments succeeded")
				}
				return
			}
			if err != nil || got.Path != test.want.Path || got.Limit == nil || *got.Limit != *test.want.Limit {
				t.Fatalf("arguments=%+v error=%v", got, err)
			}
		})
	}
}
