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
				model = newTestModel(120, 40, Options{})
				for range chunks {
					streamAssistantText(model, "word ")
				}
			}

			if len(model.entries) != 1 {
				b.Fatal("stream did not produce one block")
			}
		})
	}
}
