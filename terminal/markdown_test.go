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

func TestParseInlineMarkdownPreservesUnmatchedMarkers(t *testing.T) {
	got := parseInlineMarkdown("keep **open and *unfinished")
	if text := inlineSpanText(got); text != "keep **open and *unfinished" {
		t.Fatalf("text = %q", text)
	}
	for _, span := range got {
		if span.style != (inlineStyle{}) {
			t.Fatalf("unexpected styled span: %+v", span)
		}
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
