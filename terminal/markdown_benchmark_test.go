package terminal

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkParseInlineMarkdownPlainText(b *testing.B) {
	for _, words := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("words-%d", words), func(b *testing.B) {
			text := strings.Repeat("word ", words)
			var spans []inlineSpan

			b.ReportAllocs()
			for b.Loop() {
				spans = parseInlineMarkdown(text)
			}

			if len(spans) != 1 || spans[0].text != text {
				b.Fatal("parsed plain text changed")
			}
		})
	}
}

func BenchmarkWrapInlineMarkdown(b *testing.B) {
	for _, test := range []struct {
		name string
		text string
	}{
		{name: "words-10000", text: strings.Repeat("word ", 10_000)},
		{name: "long-word-50000", text: strings.Repeat("a", 50_000)},
	} {
		b.Run(test.name, func(b *testing.B) {
			var lines []formattedLine

			b.ReportAllocs()
			for b.Loop() {
				lines = wrapInlineMarkdown(test.text, 118)
			}

			if len(lines) == 0 {
				b.Fatal("wrapped text is empty")
			}
		})
	}
}
