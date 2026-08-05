package tool

import (
	"slices"
	"testing"
)

func TestTruncateHeadByLines(t *testing.T) {
	got := TruncateHead("one\ntwo\nthree\n", 2, 1_000)
	if got.Text != "one\ntwo\n" || !got.Truncated {
		t.Fatalf("TruncateHead() = %+v", got)
	}
	if got.OriginalLines != 3 || got.OutputLines != 2 || got.OriginalBytes != 14 || got.OutputBytes != 8 {
		t.Fatalf("TruncateHead() metadata = %+v", got)
	}
}

func TestTruncateTailByLines(t *testing.T) {
	got := TruncateTail("one\ntwo\nthree\n", 2, 1_000)
	if got.Text != "two\nthree\n" || !got.Truncated {
		t.Fatalf("TruncateTail() = %+v", got)
	}
	if got.OriginalLines != 3 || got.OutputLines != 2 {
		t.Fatalf("TruncateTail() metadata = %+v", got)
	}
}

func TestTruncateHeadByBytesPreservesUTF8(t *testing.T) {
	got := TruncateHead("éabc", 10, 3)
	if got.Text != "éa" || got.OutputBytes != 3 || !got.Truncated {
		t.Fatalf("TruncateHead() = %+v", got)
	}
}

func TestTruncateTailByBytesPreservesUTF8(t *testing.T) {
	got := TruncateTail("abcé", 10, 3)
	if got.Text != "cé" || got.OutputBytes != 3 || !got.Truncated {
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

func TestLimitMatchesPreservesOrder(t *testing.T) {
	matches := []string{"third", "first", "second"}
	got, limited := LimitMatches(matches, 2)
	if !limited || !slices.Equal(got, []string{"third", "first"}) {
		t.Fatalf("LimitMatches() = %v, %v", got, limited)
	}
	got[0] = "changed"
	if matches[0] != "third" {
		t.Fatal("LimitMatches() returned an alias of its input")
	}
}

func TestLimitEntriesPreservesOrder(t *testing.T) {
	entries := []int{4, 2, 9}
	got, limited := LimitEntries(entries, 2)
	if !limited || !slices.Equal(got, []int{4, 2}) {
		t.Fatalf("LimitEntries() = %v, %v", got, limited)
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

func TestItemLimitsAtBoundaryReturnCopies(t *testing.T) {
	matches := []string{"one", "two"}
	gotMatches, limited := LimitMatches(matches, len(matches))
	if limited || !slices.Equal(gotMatches, matches) {
		t.Fatalf("LimitMatches() = %v, %v", gotMatches, limited)
	}
	gotMatches[0] = "changed"
	if matches[0] != "one" {
		t.Fatal("LimitMatches() exact-boundary result aliases input")
	}

	entries := []int{1, 2}
	gotEntries, limited := LimitEntries(entries, len(entries))
	if limited || !slices.Equal(gotEntries, entries) {
		t.Fatalf("LimitEntries() = %v, %v", gotEntries, limited)
	}
	gotEntries[0] = 9
	if entries[0] != 1 {
		t.Fatal("LimitEntries() exact-boundary result aliases input")
	}
}

func TestZeroLimits(t *testing.T) {
	got := TruncateHead("content", 0, 100)
	if got.Text != "" || !got.Truncated {
		t.Fatalf("zero line limit = %+v", got)
	}
	matches, limited := LimitMatches([]string{"match"}, 0)
	if len(matches) != 0 || !limited {
		t.Fatalf("zero match limit = %v, %v", matches, limited)
	}
	entries, limited := LimitEntries([]string{"entry"}, 0)
	if len(entries) != 0 || !limited {
		t.Fatalf("zero entry limit = %v, %v", entries, limited)
	}
}
