package tool

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMaxLines = 2_000
	DefaultMaxBytes = 50 * 1024
)

// Truncation describes bounded text output.
type Truncation struct {
	Text          string
	Truncated     bool
	OriginalLines int
	OriginalBytes int
	OutputLines   int
	OutputBytes   int
}

// TruncateHead keeps the beginning of text within both limits.
func TruncateHead(text string, maxLines, maxBytes int) Truncation {
	return truncateText(text, maxLines, maxBytes, true)
}

// TruncateTail keeps the end of text within both limits.
func TruncateTail(text string, maxLines, maxBytes int) Truncation {
	return truncateText(text, maxLines, maxBytes, false)
}

// TruncateLine keeps the beginning of one line within maxBytes without
// splitting a UTF-8 code point.
func TruncateLine(line string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(line) <= maxBytes {
		return line, false
	}
	return prefixBytes(line, maxBytes), true
}

// LimitMatches preserves match order and reports whether matches were omitted.
func LimitMatches[T any](matches []T, max int) ([]T, bool) {
	return limitItems(matches, max)
}

// LimitEntries preserves entry order and reports whether entries were omitted.
func LimitEntries[T any](entries []T, max int) ([]T, bool) {
	return limitItems(entries, max)
}

func truncateText(text string, maxLines, maxBytes int, head bool) Truncation {
	if maxLines < 0 {
		maxLines = 0
	}
	if maxBytes < 0 {
		maxBytes = 0
	}

	lines := splitLines(text)
	limited := lines
	if len(limited) > maxLines {
		if head {
			limited = limited[:maxLines]
		} else {
			limited = limited[len(limited)-maxLines:]
		}
	}

	output := strings.Join(limited, "")
	if len(output) > maxBytes {
		if head {
			output = prefixBytes(output, maxBytes)
		} else {
			output = suffixBytes(output, maxBytes)
		}
	}

	return Truncation{
		Text:          output,
		Truncated:     len(output) != len(text),
		OriginalLines: countLines(text),
		OriginalBytes: len(text),
		OutputLines:   countLines(output),
		OutputBytes:   len(output),
	}
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
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

func prefixBytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func suffixBytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func limitItems[T any](items []T, max int) ([]T, bool) {
	if max < 0 {
		max = 0
	}
	if len(items) <= max {
		return slices.Clone(items), false
	}
	return slices.Clone(items[:max]), true
}
