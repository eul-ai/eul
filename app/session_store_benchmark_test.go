package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

func BenchmarkSessionStoreStateOnlySave(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			store := newSessionStore(b.TempDir())
			store.now = func() time.Time { return time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC) }
			checkpoint := sessionStoreBenchmarkTerminalCheckpoint(b, blockCount)
			agentCheckpoint := sessionStoreTestAgentCheckpoint(b)
			handle, err := store.Create(
				"test",
				b.TempDir(),
				modelSet{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
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

func BenchmarkSessionStoreAppendOneBlock(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			base := sessionStoreBenchmarkTerminalCheckpoint(b, blockCount)
			next := sessionStoreBenchmarkTerminalCheckpoint(b, blockCount+1)
			agentCheckpoint := sessionStoreTestAgentCheckpoint(b)
			root := b.TempDir()

			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				b.StopTimer()
				home := filepath.Join(root, fmt.Sprintf("iteration-%d", index))
				store := newSessionStore(home)
				store.now = func() time.Time { return time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC) }
				handle, err := store.Create(
					"test",
					filepath.Join(home, "workspace"),
					modelSet{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
					agent.ThinkingHigh,
					agentCheckpoint,
					subagent.EmptyCheckpoint(),
					base,
					false,
				)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()

				if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), next, false, agent.ThinkingHigh, false); err != nil {
					b.Fatal(err)
				}

				b.StopTimer()
				if err := handle.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSessionStoreList(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			store := newSessionStore(b.TempDir())
			cwd := b.TempDir()
			handle, err := store.Create(
				"test",
				cwd,
				modelSet{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
				agent.ThinkingHigh,
				sessionStoreTestAgentCheckpoint(b),
				subagent.EmptyCheckpoint(),
				sessionStoreBenchmarkTerminalCheckpoint(b, blockCount),
				false,
			)
			if err != nil {
				b.Fatal(err)
			}
			if err := handle.Close(); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				summaries, warnings, err := store.List(cwd)
				if err != nil {
					b.Fatal(err)
				}
				if len(summaries) != 1 || len(warnings) != 0 {
					b.Fatalf("summaries = %+v, warnings = %v", summaries, warnings)
				}
			}
		})
	}
}

func BenchmarkSessionStoreReplay(b *testing.B) {
	for _, blockCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("blocks-%d", blockCount), func(b *testing.B) {
			store := newSessionStore(b.TempDir())
			store.now = func() time.Time { return time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC) }
			checkpoint := sessionStoreBenchmarkTerminalCheckpoint(b, blockCount)
			agentCheckpoint := sessionStoreTestAgentCheckpoint(b)
			handle, err := store.Create(
				"test",
				b.TempDir(),
				modelSet{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
				agent.ThinkingHigh,
				agentCheckpoint,
				subagent.EmptyCheckpoint(),
				checkpoint,
				false,
			)
			if err != nil {
				b.Fatal(err)
			}
			path := handle.path
			state, err := readSessionState(sessionStatePath(path))
			if err != nil {
				b.Fatal(err)
			}
			stateInfo, err := os.Stat(sessionStatePath(path))
			if err != nil {
				b.Fatal(err)
			}
			if err := handle.Close(); err != nil {
				b.Fatal(err)
			}

			b.SetBytes(stateInfo.Size() + state.Transcript.Bytes)
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

func BenchmarkSessionStoreReplayDeltaChain(b *testing.B) {
	const (
		blockCount = 1000
		deltaCount = 100
	)
	store := newSessionStore(b.TempDir())
	agentCheckpoint := sessionStoreTestAgentCheckpoint(b)
	handle, err := store.Create(
		"test",
		b.TempDir(),
		modelSet{main: "main-model", fast: "fast-model", balanced: "balanced-model"},
		agent.ThinkingHigh,
		agentCheckpoint,
		subagent.EmptyCheckpoint(),
		sessionStoreBenchmarkTerminalCheckpoint(b, blockCount),
		false,
	)
	if err != nil {
		b.Fatal(err)
	}
	for index := range deltaCount {
		checkpoint := sessionStoreBenchmarkTerminalCheckpointVariant(b, blockCount, fmt.Sprintf(" update-%d", index))
		if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), checkpoint, false, agent.ThinkingHigh, false); err != nil {
			b.Fatal(err)
		}
	}
	path := handle.path
	if err := handle.Close(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := readSessionRecord(path); err != nil {
			b.Fatal(err)
		}
	}
}

func sessionStoreBenchmarkTerminalCheckpoint(t testing.TB, blockCount int) terminal.Checkpoint {
	t.Helper()
	return sessionStoreBenchmarkTerminalCheckpointVariant(t, blockCount, "")
}

func sessionStoreBenchmarkTerminalCheckpointVariant(t testing.TB, blockCount int, lastSuffix string) terminal.Checkpoint {
	t.Helper()
	blocks := make([]map[string]any, blockCount)
	body := strings.Repeat("deterministic transcript content ", 6)
	for index := range blocks {
		text := fmt.Sprintf("message-%04d %s", index, body)
		if index == blockCount-1 {
			text += lastSuffix
		}
		blocks[index] = map[string]any{
			"kind": index % 2,
			"text": text,
		}
	}
	encoded, err := json.Marshal(map[string]any{"version": 2, "blocks": blocks})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint terminal.Checkpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
