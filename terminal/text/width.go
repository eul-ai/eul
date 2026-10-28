package text

import (
	"strings"
	"unicode"
)

func Wrap(value string, width int) []string {
	if width <= 0 {
		return nil
	}
	value = strings.ReplaceAll(value, "\t", "    ")
	paragraphs := strings.Split(value, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}

		var line strings.Builder
		lineWidth := 0
		for _, character := range paragraph {
			characterWidth := RuneWidth(character)
			if lineWidth > 0 && lineWidth+characterWidth > width {
				lines = append(lines, line.String())
				line.Reset()
				lineWidth = 0
			}
			line.WriteRune(character)
			lineWidth += characterWidth
		}
		lines = append(lines, line.String())
	}
	return lines
}

func Truncate(value string, width int, ellipsis bool) string {
	if width <= 0 {
		return ""
	}
	if Width(value) <= width {
		return value
	}

	limit := width
	suffix := ""
	if ellipsis && width > 1 {
		limit--
		suffix = "…"
	}
	var result strings.Builder
	used := 0
	for _, character := range value {
		characterWidth := RuneWidth(character)
		if used+characterWidth > limit {
			break
		}
		result.WriteRune(character)
		used += characterWidth
	}
	result.WriteString(suffix)
	return result.String()
}

func Slice(value string, start, end int) string {
	if start >= end {
		return ""
	}

	var result strings.Builder
	position := 0
	selectedBase := false
	for _, character := range value {
		width := RuneWidth(character)
		if width == 0 {
			if selectedBase {
				result.WriteRune(character)
			}
			continue
		}

		next := position + width
		selectedBase = position < end && next > start
		if selectedBase {
			result.WriteRune(character)
		}
		position = next
	}
	return result.String()
}

func Width(value string) int {
	width := 0
	for _, character := range value {
		width += RuneWidth(character)
	}
	return width
}

func RuneWidth(character rune) int {
	if character >= ' ' && character <= '~' {
		return 1
	}
	if character == 0 || unicode.Is(unicode.Mn, character) || unicode.Is(unicode.Me, character) || unicode.Is(unicode.Cf, character) {
		return 0
	}
	if unicode.IsControl(character) {
		return 0
	}
	if isWideRune(character) {
		return 2
	}
	return 1
}

func isWideRune(character rune) bool {
	return character >= 0x1100 && (character <= 0x115f ||
		character == 0x2329 || character == 0x232a ||
		character >= 0x2e80 && character <= 0xa4cf && character != 0x303f ||
		character >= 0xac00 && character <= 0xd7a3 ||
		character >= 0xf900 && character <= 0xfaff ||
		character >= 0xfe10 && character <= 0xfe19 ||
		character >= 0xfe30 && character <= 0xfe6f ||
		character >= 0xff00 && character <= 0xff60 ||
		character >= 0xffe0 && character <= 0xffe6 ||
		character >= 0x1f300 && character <= 0x1faff ||
		character >= 0x20000 && character <= 0x3fffd)
}
