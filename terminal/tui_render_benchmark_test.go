package terminal

import (
	"fmt"
	"testing"
)

func BenchmarkTUIResizeTranscript(b *testing.B) {
	for _, blockCount := range []int{1000, 10_000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			model := newTUIModel(120, 40, Options{Config: Config{Model: "benchmark"}})
			for index := range blockCount {
				model.appendBlock(blockAssistant, fmt.Sprintf("Response **%d** with `code` and enough text to exercise markdown wrapping across the conversation.", index))
			}
			renderer := &tuiRenderer{}
			normalizeViewport(model, renderer)
			_, frame := renderer.renderPending(model, false)
			renderer.commit(frame)

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				switch model.width {
				case 119:
					model.width = 120
				default:
					model.width = 119
				}
				normalizeViewport(model, renderer)
				_, frame = renderer.renderPending(model, false)
				renderer.commit(frame)
			}
		})
	}
}

func BenchmarkTUIRenderStreamingTranscript(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			model := newTUIModel(120, 40, Options{Config: Config{Model: "benchmark"}})
			for index := 0; index < blockCount-1; index++ {
				model.appendBlock(blockAssistant, fmt.Sprintf("Response **%d** with `code` and enough text to exercise markdown wrapping across the conversation.", index))
			}
			model.appendStream(blockAssistant, "Active response with `streaming` text.")

			renderer := &tuiRenderer{}
			_, frame := renderer.renderPending(model, false)
			renderer.commit(frame)
			variants := []string{
				"Active response with `streaming` text and one delta.",
				"Active response with `streaming` text and another delta.",
			}

			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				model.blocks[len(model.blocks)-1].text = variants[index%len(variants)]
				model.conversationChanged()
				_, frame = renderer.renderPending(model, false)
				renderer.commit(frame)
			}
		})
	}
}
