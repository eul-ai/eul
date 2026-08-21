package terminal

import (
	"fmt"
	"testing"
)

func BenchmarkTUIAppendStream(b *testing.B) {
	for _, chunks := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("chunks-%d", chunks), func(b *testing.B) {
			var model *tuiModel

			b.ReportAllocs()
			for b.Loop() {
				model = newTUIModel(120, 40, Options{})
				for range chunks {
					model.appendStream(blockAssistant, "word ")
				}
			}

			if len(model.blocks) != 1 {
				b.Fatal("stream did not produce one block")
			}
		})
	}
}
