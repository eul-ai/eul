package terminal

import (
	"reflect"
	"testing"
)

func TestParseInlineMarkdown(t *testing.T) {
	got := parseInlineMarkdown("plain **bold** *italic* ***both***")
	want := []inlineSpan{
		{text: "plain ", style: inlineStyle{}},
		{text: "bold", style: inlineStyle{bold: true}},
		{text: " ", style: inlineStyle{}},
		{text: "italic", style: inlineStyle{italic: true}},
		{text: " ", style: inlineStyle{}},
		{text: "both", style: inlineStyle{bold: true, italic: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownLinks(t *testing.T) {
	got := parseInlineMarkdown("Read [the **docs**](https://example.com/docs) or visit https://example.com.")
	want := []inlineSpan{
		{text: "Read ", style: inlineStyle{}},
		{text: "the ", style: inlineStyle{link: "https://example.com/docs"}},
		{text: "docs", style: inlineStyle{bold: true, link: "https://example.com/docs"}},
		{text: " or visit ", style: inlineStyle{}},
		{text: "https://example.com", style: inlineStyle{link: "https://example.com"}},
		{text: ".", style: inlineStyle{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownLinkDestinations(t *testing.T) {
	tests := []struct {
		input string
		want  []inlineSpan
	}{
		{
			input: "[https://inner.example](https://outer.example)",
			want:  []inlineSpan{{text: "https://inner.example", style: inlineStyle{link: "https://outer.example"}}},
		},
		{
			input: "https://example.com/foo(bar)",
			want:  []inlineSpan{{text: "https://example.com/foo(bar)", style: inlineStyle{link: "https://example.com/foo(bar)"}}},
		},
		{
			input: "<mailto:user@example.com>",
			want:  []inlineSpan{{text: "mailto:user@example.com", style: inlineStyle{link: "mailto:user@example.com"}}},
		},
	}
	for _, test := range tests {
		if got := parseInlineMarkdown(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("parseInlineMarkdown(%q) = %+v, want %+v", test.input, got, test.want)
		}
	}
}

func TestParseInlineMarkdownCodeSpans(t *testing.T) {
	got := parseInlineMarkdown("Use `**name**` and ``a`b``")
	want := []inlineSpan{
		{text: "Use ", style: inlineStyle{}},
		{text: "**name**", style: inlineStyle{code: true}},
		{text: " and ", style: inlineStyle{}},
		{text: "a`b", style: inlineStyle{code: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownPreservesCodeFences(t *testing.T) {
	got := parseInlineMarkdown("```bash\nabc\n```")
	want := []inlineSpan{{text: "```bash\nabc\n```", style: inlineStyle{}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownFencedCode(t *testing.T) {
	got := wrapMarkdown("before **bold**\n```go\nfmt.Println(`value`)\n\n```\nafter", 80)
	want := []formattedLine{
		{text: "before bold", spans: []inlineSpan{
			{text: "before ", style: inlineStyle{}},
			{text: "bold", style: inlineStyle{bold: true}},
		}},
		{text: "fmt.Println(`value`)", fencedCode: true},
		{fencedCode: true},
		{text: "after", spans: []inlineSpan{{text: "after", style: inlineStyle{}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownFencedCodeUsesAvailableWidth(t *testing.T) {
	got := wrapMarkdown("```\nabcdefgh\n```", 6)
	want := []formattedLine{
		{text: "abcdef", fencedCode: true},
		{text: "gh", breakBefore: lineBreak{continuation: true}, fencedCode: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownNestedCodeInEmphasis(t *testing.T) {
	got := parseInlineMarkdown("**`backend/codex/api/models.go`** and *`SIGWINCH`*")
	want := []inlineSpan{
		{text: "backend/codex/api/models.go", style: inlineStyle{bold: true, code: true}},
		{text: " and ", style: inlineStyle{}},
		{text: "SIGWINCH", style: inlineStyle{italic: true, code: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownPreservesUnmatchedMarkers(t *testing.T) {
	got := parseInlineMarkdown("keep **open and *unfinished and `code")
	if text := inlineSpanText(got); text != "keep **open and *unfinished and `code" {
		t.Fatalf("text = %q", text)
	}
	for _, span := range got {
		if span.style != (inlineStyle{}) {
			t.Fatalf("unexpected styled span: %+v", span)
		}
	}
}

func TestWrapInlineMarkdownCodeMarkersDoNotUseWidth(t *testing.T) {
	lines := wrapInlineMarkdown("`abcde`", 5)
	if len(lines) != 1 || lines[0].text != "abcde" || len(lines[0].spans) != 1 || !lines[0].spans[0].style.code {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestWrapInlineMarkdownUsesRenderedWidth(t *testing.T) {
	lines := wrapInlineMarkdown("**abcd** *ef*", 5)
	if len(lines) != 2 || lines[0].text != "abcd" || lines[1].text != "ef" {
		t.Fatalf("lines = %+v", lines)
	}
	if len(lines[0].spans) != 1 || !lines[0].spans[0].style.bold {
		t.Fatalf("first line spans = %+v", lines[0].spans)
	}
	if len(lines[1].spans) != 1 || !lines[1].spans[0].style.italic || lines[1].breakBefore.separator != " " {
		t.Fatalf("second line = %+v", lines[1])
	}
}

func TestWrapInlineMarkdownUsesWordBoundaries(t *testing.T) {
	lines := wrapInlineMarkdown("alpha beta gamma", 8)
	want := []string{"alpha", "beta", "gamma"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v", lines)
	}
	for index, text := range want {
		if lines[index].text != text {
			t.Fatalf("line %d = %+v, want text %q", index, lines[index], text)
		}
		if index > 0 && (!lines[index].breakBefore.continuation || lines[index].breakBefore.separator != " ") {
			t.Fatalf("line %d does not preserve its soft-wrap separator: %+v", index, lines[index])
		}
	}
}

func TestWrapInlineMarkdownHardWrapsLongWords(t *testing.T) {
	lines := wrapInlineMarkdown("**abcdefgh**", 5)
	if len(lines) != 2 || lines[0].text != "abcde" || lines[1].text != "fgh" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[1].breakBefore.continuation || lines[1].breakBefore.separator != "" {
		t.Fatalf("long word continuation = %+v", lines[1])
	}
	for index, line := range lines {
		if len(line.spans) != 1 || !line.spans[0].style.bold {
			t.Fatalf("line %d lost its style: %+v", index, line)
		}
	}
}

func TestWrapInlineMarkdownUsesCellWidthAtWordBoundaries(t *testing.T) {
	lines := wrapInlineMarkdown("界界 test", 5)
	if len(lines) != 2 || lines[0].text != "界界" || lines[1].text != "test" || lines[1].breakBefore.separator != " " {
		t.Fatalf("lines = %+v", lines)
	}
}
