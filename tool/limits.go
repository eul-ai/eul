package tool

import (
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxLines = 2_000
	defaultMaxBytes = 50 * 1024
)

type truncation struct {
	text      string
	truncated bool
}

func truncateHead(text string, maxLines, maxBytes int) truncation {
	return truncateText(text, maxLines, maxBytes, true)
}

func truncateTail(text string, maxLines, maxBytes int) truncation {
	return truncateText(text, maxLines, maxBytes, false)
}

func truncateLine(line string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(line) <= maxBytes {
		return line, false
	}
	return prefixBytes(line, maxBytes), true
}

func truncateText(text string, maxLines, maxBytes int, head bool) truncation {
	if maxLines < 0 {
		maxLines = 0
	}
	if maxBytes < 0 {
		maxBytes = 0
	}

	lines := limitLines(splitLines(text), maxLines, head)
	output := limitBytes(strings.Join(lines, ""), maxBytes, head)
	return truncation{text: output, truncated: len(output) != len(text)}
}

func limitLines(lines []string, maximum int, head bool) []string {
	if len(lines) <= maximum {
		return lines
	}
	if head {
		return lines[:maximum]
	}
	return lines[len(lines)-maximum:]
}

func limitBytes(text string, maximum int, head bool) string {
	if len(text) <= maximum {
		return text
	}
	if head {
		return prefixBytes(text, maximum)
	}
	return suffixBytes(text, maximum)
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
