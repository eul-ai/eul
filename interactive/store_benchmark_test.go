package interactive

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool/subagent"
)

func BenchmarkSessionStoreSave(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			store := newSessionStore(b.TempDir())
			store.now = func() time.Time { return time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC) }
			checkpoint := sessionStoreBenchmarkTerminalCheckpoint(b, blockCount)
			agentCheckpoint := sessionStoreTestAgentCheckpoint(b)
			handle, err := store.Create(
				"test",
				b.TempDir(),
				modelSelection{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
				agent.ThinkingHigh,
				agentCheckpoint,
				subagent.EmptyCheckpoint(),
				checkpoint,
				false,
			)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = handle.Close() })
			if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), checkpoint, false, agent.ThinkingHigh, false); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(handle.path)
			if err != nil {
				b.Fatal(err)
			}

			b.SetBytes(info.Size())
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), checkpoint, false, agent.ThinkingHigh, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSessionStoreRead(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			store := newSessionStore(b.TempDir())
			store.now = func() time.Time { return time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC) }
			checkpoint := sessionStoreBenchmarkTerminalCheckpoint(b, blockCount)
			agentCheckpoint := sessionStoreTestAgentCheckpoint(b)
			handle, err := store.Create(
				"test",
				b.TempDir(),
				modelSelection{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
				agent.ThinkingHigh,
				agentCheckpoint,
				subagent.EmptyCheckpoint(),
				checkpoint,
				false,
			)
			if err != nil {
				b.Fatal(err)
			}
			if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), checkpoint, false, agent.ThinkingHigh, false); err != nil {
				b.Fatal(err)
			}
			path := handle.path
			if err := handle.Close(); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}

			b.SetBytes(info.Size())
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				record, err := readSessionRecord(path)
				if err != nil {
					b.Fatal(err)
				}
				if !record.Terminal.Initialized() || record.Terminal.Description() == "" {
					b.Fatal("terminal checkpoint was not restored")
				}
			}
		})
	}
}

func sessionStoreBenchmarkTerminalCheckpoint(t testing.TB, blockCount int) terminal.Checkpoint {
	t.Helper()
	blocks := make([]map[string]any, blockCount)
	body := strings.Repeat("deterministic transcript content ", 6)
	for index := range blocks {
		blocks[index] = map[string]any{
			"kind": index % 2,
			"text": fmt.Sprintf("message-%04d %s", index, body),
		}
	}
	encoded, err := json.Marshal(map[string]any{"version": 1, "blocks": blocks})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint terminal.Checkpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
