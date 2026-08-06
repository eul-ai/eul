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

	return truncation{text: output, truncated: len(output) != len(text)}
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
