package tool

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseStreamingJSONObjectRecoversPartialValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{name: "empty", raw: "", want: map[string]any{}},
		{name: "open object", raw: `{"path":"file.go"`, want: map[string]any{"path": "file.go"}},
		{name: "open string", raw: `{"content":"first\nsec`, want: map[string]any{"content": "first\nsec"}},
		{name: "nested", raw: `{"outer":{"value":"yes`, want: map[string]any{"outer": map[string]any{"value": "yes"}}},
		{name: "array", raw: `{"tasks":["one","tw`, want: map[string]any{"tasks": []any{"one", "tw"}}},
		{name: "incomplete literal", raw: `{"value":tru`, want: map[string]any{}},
		{name: "trailing object", raw: `{"path":"first"}{"path":"second"}`, want: map[string]any{}},
		{name: "null root", raw: `null`, want: map[string]any{}},
		{name: "malformed root", raw: `not json`, want: map[string]any{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseStreamingJSONObject(test.raw); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseStreamingJSONObject(%q) = %#v, want %#v", test.raw, got, test.want)
			}
		})
	}
}

func TestParseStreamingJSONObjectHandlesEveryWriteArgumentBoundary(t *testing.T) {
	raw := `{"path":"demo.go","content":"package main\n\nfunc main() {\n\tprintln(\"☺\")\n}\n"}`
	for boundary := 0; boundary <= len(raw); boundary++ {
		got := parseStreamingJSONObject(raw[:boundary])
		path, _ := got["path"].(string)
		if path != "" && path != "demo.go" && path != "d" && path != "de" && path != "dem" && path != "demo" && path != "demo." && path != "demo.g" {
			t.Fatalf("boundary %d produced invalid path %q", boundary, path)
		}
		content, _ := got["content"].(string)
		fullContent := "package main\n\nfunc main() {\n\tprintln(\"☺\")\n}\n"
		if !strings.HasPrefix(fullContent, content) {
			t.Fatalf("boundary %d produced invalid content %q", boundary, content)
		}
	}

	got := parseStreamingJSONObject(raw)
	if got["path"] != "demo.go" || got["content"] != "package main\n\nfunc main() {\n\tprintln(\"☺\")\n}\n" {
		t.Fatalf("complete parse = %#v", got)
	}
}

func TestParseStreamingJSONObjectWaitsForCompleteEscapes(t *testing.T) {
	prefix := `{"content":"before `
	for _, suffix := range []string{`\`, `\u`, `\u2`, `\u26`, `\u263`} {
		got := parseStreamingJSONObject(prefix + suffix)
		if got["content"] != "before " {
			t.Fatalf("suffix %q content = %#v", suffix, got["content"])
		}
	}
	if got := parseStreamingJSONObject(prefix + `\u263A`); got["content"] != "before ☺" {
		t.Fatalf("unicode content = %#v", got["content"])
	}
	if got := parseStreamingJSONObject(prefix + `\uD83D`); got["content"] != "before " {
		t.Fatalf("partial surrogate content = %#v", got["content"])
	}
	if got := parseStreamingJSONObject(prefix + `\uD83D\uDE00`); got["content"] != "before 😀" {
		t.Fatalf("surrogate pair content = %#v", got["content"])
	}
}
