package terminal

import (
	"fmt"
	"testing"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
)

func BenchmarkTUIResizeTranscript(b *testing.B) {
	for _, blockCount := range []int{1000, 10_000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			model := newTestModel(120, 40, &fakeSession{settings: session.Settings{Models: backend.Models{Main: "benchmark"}}})
			for index := range blockCount {
				model.appendEntry(session.EntryAssistant, fmt.Sprintf("Response **%d** with `code` and enough text to exercise markdown wrapping across the conversation.", index))
			}
			renderer := &tuiRenderer{}
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
				_, frame = renderer.renderPending(model, false)
				renderer.commit(frame)
			}
		})
	}
}

func BenchmarkTUIRenderStreamingTranscript(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			model := newTestModel(120, 40, &fakeSession{settings: session.Settings{Models: backend.Models{Main: "benchmark"}}})
			for index := 0; index < blockCount-1; index++ {
				model.appendEntry(session.EntryAssistant, fmt.Sprintf("Response **%d** with `code` and enough text to exercise markdown wrapping across the conversation.", index))
			}
			streamAssistantText(model, "Active response with `streaming` text.")

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
				model.entries[len(model.entries)-1].Text = variants[index%len(variants)]
				model.conversationChanged()
				_, frame = renderer.renderPending(model, false)
				renderer.commit(frame)
			}
		})
	}
}
