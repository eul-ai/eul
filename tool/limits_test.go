package tool

import (
	"strings"
	"testing"
)

func TestTruncateHeadByLines(t *testing.T) {
	got := truncateHead("one\ntwo\nthree\n", 2, 1_000)
	if got.text != "one\ntwo\n" || !got.truncated {
		t.Fatalf("truncateHead() = %+v", got)
	}
}

func TestTruncateTailByLines(t *testing.T) {
	got := truncateTail("one\ntwo\nthree\n", 2, 1_000)
	if got.text != "two\nthree\n" || !got.truncated {
		t.Fatalf("truncateTail() = %+v", got)
	}
}

func TestTruncateHeadByBytesPreservesUTF8(t *testing.T) {
	got := truncateHead("éabc", 10, 3)
	if got.text != "éa" || !got.truncated {
		t.Fatalf("truncateHead() = %+v", got)
	}
}

func TestTruncateTailByBytesPreservesUTF8(t *testing.T) {
	got := truncateTail("abcé", 10, 3)
	if got.text != "cé" || !got.truncated {
		t.Fatalf("truncateTail() = %+v", got)
	}
}

func TestTruncateLine(t *testing.T) {
	got, truncated := truncateLine("éabc", 4)
	if got != "éab" || !truncated {
		t.Fatalf("truncateLine() = %q, %v", got, truncated)
	}
	got, truncated = truncateLine("short", 10)
	if got != "short" || truncated {
		t.Fatalf("truncateLine() unbounded = %q, %v", got, truncated)
	}
}

func TestTextLimitsAreIndependent(t *testing.T) {
	byLines := truncateHead("a\nb\nc\n", 2, 1_000)
	if byLines.text != "a\nb\n" {
		t.Fatalf("line limit result = %q", byLines.text)
	}
	byBytes := truncateHead("a\nb\nc\n", 100, 3)
	if byBytes.text != "a\nb" {
		t.Fatalf("byte limit result = %q", byBytes.text)
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
			if got := truncateHead(test.text, test.lines, test.bytes).text; got != test.wantHead {
				t.Fatalf("truncateHead() = %q, want %q", got, test.wantHead)
			}
			if got := truncateTail(test.text, test.lines, test.bytes).text; got != test.wantTail {
				t.Fatalf("truncateTail() = %q, want %q", got, test.wantTail)
			}
		})
	}
}

func TestZeroLimits(t *testing.T) {
	got := truncateHead("content", 0, 100)
	if got.text != "" || !got.truncated {
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
