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

func TestParseInlineMarkdownNestedCodeInEmphasis(t *testing.T) {
	got := parseInlineMarkdown("**`backend/openai/codex/models.go`** and *`SIGWINCH`*")
	want := []inlineSpan{
		{text: "backend/openai/codex/models.go", style: inlineStyle{bold: true, code: true}},
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
	if len(lines) != 2 || lines[0].text != "abcd " || lines[1].text != "ef" {
		t.Fatalf("lines = %+v", lines)
	}
	if len(lines[0].spans) != 2 || !lines[0].spans[0].style.bold {
		t.Fatalf("first line spans = %+v", lines[0].spans)
	}
	if len(lines[1].spans) != 1 || !lines[1].spans[0].style.italic {
		t.Fatalf("second line spans = %+v", lines[1].spans)
	}
}
