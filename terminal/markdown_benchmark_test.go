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
