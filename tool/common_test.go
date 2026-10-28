package tool

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundKeepsTextWithinLimits(t *testing.T) {
	if got := bound("one\ntwo\n", "", false); got != "one\ntwo\n" {
		t.Fatalf("bound() = %q", got)
	}
	if got := bound("short", "note", false); got != "short\n[note]\n" {
		t.Fatalf("bound() head notice = %q", got)
	}
	if got := bound("short", "note", true); got != "[note]\nshort" {
		t.Fatalf("bound() tail notice = %q", got)
	}
}

func TestBoundTruncatesLines(t *testing.T) {
	var text strings.Builder
	for index := range defaultMaxLines + 10 {
		fmt.Fprintf(&text, "line-%d\n", index)
	}

	head := bound(text.String(), "", false)
	if countLines(head) != defaultMaxLines || !strings.HasPrefix(head, "line-0\n") || !strings.HasSuffix(head, "[output truncated]\n") {
		t.Fatalf("head lines=%d suffix=%q", countLines(head), head[len(head)-40:])
	}

	tail := bound(text.String(), "", true)
	last := fmt.Sprintf("line-%d\n", defaultMaxLines+9)
	if countLines(tail) != defaultMaxLines || !strings.HasPrefix(tail, "[earlier output truncated]\n") || !strings.HasSuffix(tail, last) {
		t.Fatalf("tail lines=%d prefix=%q", countLines(tail), tail[:40])
	}
}

func TestBoundTruncatesBytesOnRuneBoundaries(t *testing.T) {
	text := strings.Repeat("é", defaultMaxBytes)
	for _, keepTail := range []bool{false, true} {
		got := bound(text, "", keepTail)
		if len(got) > defaultMaxBytes || !utf8.ValidString(got) || !strings.Contains(got, "é") {
			t.Fatalf("keepTail=%t bytes=%d valid=%t", keepTail, len(got), utf8.ValidString(got))
		}
	}
}

func TestByteLimitsPreserveUTF8(t *testing.T) {
	for _, test := range []struct {
		got  string
		want string
	}{
		{got: prefixBytes("éabc", 4), want: "éab"},
		{got: prefixBytes("éabc", 1), want: ""},
		{got: prefixBytes("short", 10), want: "short"},
		{got: suffixBytes("abcé", 3), want: "cé"},
		{got: suffixBytes("abcé", 1), want: ""},
		{got: suffixBytes("short", 10), want: "short"},
	} {
		if test.got != test.want {
			t.Fatalf("got %q, want %q", test.got, test.want)
		}
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
