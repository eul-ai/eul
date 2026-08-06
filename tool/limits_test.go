package tool

import (
	"strings"
	"testing"
)

func TestTruncateHeadByLines(t *testing.T) {
	got := TruncateHead("one\ntwo\nthree\n", 2, 1_000)
	if got.Text != "one\ntwo\n" || !got.Truncated {
		t.Fatalf("TruncateHead() = %+v", got)
	}
}

func TestTruncateTailByLines(t *testing.T) {
	got := TruncateTail("one\ntwo\nthree\n", 2, 1_000)
	if got.Text != "two\nthree\n" || !got.Truncated {
		t.Fatalf("TruncateTail() = %+v", got)
	}
}

func TestTruncateHeadByBytesPreservesUTF8(t *testing.T) {
	got := TruncateHead("éabc", 10, 3)
	if got.Text != "éa" || !got.Truncated {
		t.Fatalf("TruncateHead() = %+v", got)
	}
}

func TestTruncateTailByBytesPreservesUTF8(t *testing.T) {
	got := TruncateTail("abcé", 10, 3)
	if got.Text != "cé" || !got.Truncated {
		t.Fatalf("TruncateTail() = %+v", got)
	}
}

func TestTruncateLine(t *testing.T) {
	got, truncated := TruncateLine("éabc", 4)
	if got != "éab" || !truncated {
		t.Fatalf("TruncateLine() = %q, %v", got, truncated)
	}
	got, truncated = TruncateLine("short", 10)
	if got != "short" || truncated {
		t.Fatalf("TruncateLine() unbounded = %q, %v", got, truncated)
	}
}

func TestTextLimitsAreIndependent(t *testing.T) {
	byLines := TruncateHead("a\nb\nc\n", 2, 1_000)
	if byLines.Text != "a\nb\n" {
		t.Fatalf("line limit result = %q", byLines.Text)
	}
	byBytes := TruncateHead("a\nb\nc\n", 100, 3)
	if byBytes.Text != "a\nb" {
		t.Fatalf("byte limit result = %q", byBytes.Text)
	}
}

func TestTextLimitEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		lines    int
		bytes    int
		wantHead string
		wantTail string
	}{
		{name: "empty", text: "", lines: 1, bytes: 1, wantHead: "", wantTail: ""},
		{name: "exact", text: "a\nb", lines: 2, bytes: 3, wantHead: "a\nb", wantTail: "a\nb"},
		{name: "no final newline", text: "one\ntwo\nthree", lines: 2, bytes: 100, wantHead: "one\ntwo\n", wantTail: "two\nthree"},
		{name: "both limits active", text: "aa\nbb\ncc\n", lines: 2, bytes: 4, wantHead: "aa\nb", wantTail: "\ncc\n"},
		{name: "smaller than rune", text: "é", lines: 1, bytes: 1, wantHead: "", wantTail: ""},
		{name: "negative limits", text: "text", lines: -1, bytes: -1, wantHead: "", wantTail: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TruncateHead(test.text, test.lines, test.bytes).Text; got != test.wantHead {
				t.Fatalf("TruncateHead() = %q, want %q", got, test.wantHead)
			}
			if got := TruncateTail(test.text, test.lines, test.bytes).Text; got != test.wantTail {
				t.Fatalf("TruncateTail() = %q, want %q", got, test.wantTail)
			}
		})
	}
}

func TestZeroLimits(t *testing.T) {
	got := TruncateHead("content", 0, 100)
	if got.Text != "" || !got.Truncated {
		t.Fatalf("zero line limit = %+v", got)
	}
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	lines := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		lines++
	}
	return lines
}
