package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

func TestSessionStorePartitionsListsAndResolvesSessions(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	store := newSessionStore(home)
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	agentCheckpoint := sessionStoreTestAgentCheckpoint(t)

	first, err := store.Create("test", cwd, modelSet{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingHigh, agentCheckpoint, subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "first prompt\nmore"), true)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.record.ID
	if first.record.Provider != "test" || first.record.FastModel != "fast-model" || first.record.BalancedModel != "balanced-model" || !first.record.FastMode {
		t.Fatalf("provider and models = %+v", first.record)
	}
	if err := first.Save(agentCheckpoint, first.record.Subagent, first.record.Terminal, true, agent.ThinkingHigh, false); err != nil {
		t.Fatal(err)
	}
	if first.record.FastMode {
		t.Fatalf("fast mode was not updated: %+v", first.record)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	second, err := store.Create("test", cwd, modelSet{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, agentCheckpoint, subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "second prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	secondID := second.record.ID
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	workspace := store.workspaceDirectory(cwd)
	if filepath.Dir(workspace) != filepath.Join(home, "sessions") || filepath.Base(workspace) == filepath.Base(cwd) {
		t.Fatalf("workspace path = %q", workspace)
	}
	for _, path := range []string{filepath.Join(home, "sessions"), workspace} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s permissions = %o", path, info.Mode().Perm())
		}
	}
	sessionDirectory := filepath.Join(workspace, firstID)
	directoryInfo, err := os.Stat(sessionDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("session directory permissions = %o", directoryInfo.Mode().Perm())
	}
	for _, name := range []string{sessionStateFileName, sessionTranscriptAName, sessionLockFileName} {
		fileInfo, err := os.Stat(filepath.Join(sessionDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %o", name, fileInfo.Mode().Perm())
		}
	}

	locked, err := store.Open(context.Background(), cwd, secondID)
	if err != nil {
		t.Fatal(err)
	}
	summaries, warnings, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(summaries) != 1 || summaries[0].ID != firstID || summaries[0].Description != "first prompt" || !summaries[0].Active {
		t.Fatalf("summaries = %+v", summaries)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}

	mostRecent, err := store.Open(context.Background(), cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if mostRecent.record.ID != secondID {
		t.Fatalf("most recent ID = %q", mostRecent.record.ID)
	}
	if _, err := store.Open(context.Background(), cwd, secondID); !errors.Is(err, errSessionInUse) {
		t.Fatalf("lock contention error = %v", err)
	}
	if err := mostRecent.Close(); err != nil {
		t.Fatal(err)
	}

	otherCWD := t.TempDir()
	explicit, err := store.Open(context.Background(), otherCWD, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.record.WorkingDirectory != cwd {
		t.Fatalf("stored cwd = %q", explicit.record.WorkingDirectory)
	}
	if err := explicit.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStateKeepsModelJSONFields(t *testing.T) {
	models := modelSet{main: "main-model", fast: "fast-model", balanced: "balanced-model"}
	_, terminalState, err := terminal.SplitCheckpoint(terminal.EmptyCheckpoint())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sessionState{
		sessionMetadata: sessionMetadata{
			Model:         models.main,
			FastModel:     models.fast,
			BalancedModel: models.balanced,
		},
		Agent:      sessionStoreTestAgentCheckpoint(t),
		Subagent:   subagent.EmptyCheckpoint(),
		Terminal:   terminalState,
		Transcript: sessionTranscriptHead{Slot: initialTranscriptSlot},
	})
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"model":          models.main,
		"fast_model":     models.fast,
		"balanced_model": models.balanced,
	} {
		var got string
		if err := json.Unmarshal(fields[key], &got); err != nil {
			t.Fatalf("field %q: %v", key, err)
		}
		if got != want {
			t.Fatalf("field %q = %q, want %q", key, got, want)
		}
	}
	if _, exists := fields["models"]; exists {
		t.Fatal("session state added a models JSON field")
	}
}

func TestSessionStoreIgnoresLegacySessionFiles(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	workspace := store.workspaceDirectory(cwd)
	if err := secureSessionDirectory(workspace); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(workspace, id+".json"), []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, warnings, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 || len(warnings) != 0 {
		t.Fatalf("summaries = %+v, warnings = %v", summaries, warnings)
	}
	if _, err := store.Open(context.Background(), cwd, id); err == nil {
		t.Fatal("legacy session file was opened")
	}
}

func TestSessionStoreRequiresCompleteModelSet(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	for name, models := range map[string]modelSet{
		"main":     {fast: "fast-model", balanced: "balanced-model"},
		"fast":     {main: "main-model", balanced: "balanced-model"},
		"balanced": {main: "main-model", fast: "fast-model"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Create("test", cwd, models, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), terminal.EmptyCheckpoint(), false)
			if err == nil {
				t.Fatal("incomplete model set was accepted")
			}
		})
	}
}

func TestSessionStoreDoesNotPersistOrListEmptySessions(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), terminal.EmptyCheckpoint(), false)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := sessionLockPath(handle.path)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionStatePath(handle.path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty session state error = %v", err)
	}
	if err := handle.Save(sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), terminal.EmptyCheckpoint(), false, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionStatePath(handle.path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved empty session state error = %v", err)
	}
	summaries, warnings, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(summaries) != 0 {
		t.Fatalf("empty session summaries = %+v", summaries)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty session lock error = %v", err)
	}
}

func TestSessionStoreAppendsOnlyTranscriptChanges(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	agentCheckpoint := sessionStoreTestAgentCheckpoint(t)
	prompt := strings.Repeat("prompt ", 200)
	initial := sessionStoreTestTerminalCheckpoint(t, prompt)
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "model", balanced: "model"}, agent.ThinkingMedium, agentCheckpoint, subagent.EmptyCheckpoint(), initial, false)
	if err != nil {
		t.Fatal(err)
	}

	state, err := readSessionState(sessionStatePath(handle.path))
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := sessionTranscriptPath(handle.path, state.Transcript.Slot)
	initialTranscript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), initial, true, agent.ThinkingHigh, true); err != nil {
		t.Fatal(err)
	}
	unchangedTranscript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchangedTranscript) != string(initialTranscript) {
		t.Fatal("state-only save changed the transcript")
	}

	next := sessionStoreTestTerminalBlocks(t, []map[string]any{
		{"kind": 0, "text": prompt},
		{"kind": 1, "text": "answer"},
	})
	if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), next, false, agent.ThinkingHigh, true); err != nil {
		t.Fatal(err)
	}
	state, err = readSessionState(sessionStatePath(handle.path))
	if err != nil {
		t.Fatal(err)
	}
	if state.Transcript.BlockCount != 2 || state.Transcript.BaseBytes != int64(len(initialTranscript)) || state.Transcript.DeltaBytes <= 0 {
		t.Fatalf("transcript head = %+v", state.Transcript)
	}
	transcript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	records := strings.Split(strings.TrimSpace(string(transcript)), "\n")
	if len(records) != 2 || strings.Contains(records[1], "prompt") || !strings.Contains(records[1], "answer") {
		t.Fatalf("transcript records = %q", records)
	}

	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	encoded, err := json.Marshal(reopened.record.Terminal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "prompt") || !strings.Contains(string(encoded), "answer") {
		t.Fatalf("restored terminal checkpoint = %s", encoded)
	}
}

func TestSessionStoreCompactsTranscriptUsingInactiveSlot(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	agentCheckpoint := sessionStoreTestAgentCheckpoint(t)
	initial := sessionStoreTestTerminalCheckpoint(t, "prompt")
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "model", balanced: "model"}, agent.ThinkingMedium, agentCheckpoint, subagent.EmptyCheckpoint(), initial, false)
	if err != nil {
		t.Fatal(err)
	}

	second := sessionStoreTestTerminalBlocks(t, []map[string]any{
		{"kind": 0, "text": "prompt"},
		{"kind": 1, "text": strings.Repeat("second ", 200)},
	})
	if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), second, false, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	state, err := readSessionState(sessionStatePath(handle.path))
	if err != nil {
		t.Fatal(err)
	}
	if state.Transcript.Slot != "b" || state.Transcript.DeltaBytes != 0 || state.Transcript.BaseBytes != state.Transcript.Bytes || state.Transcript.BlockCount != 2 {
		t.Fatalf("compacted transcript head = %+v", state.Transcript)
	}
	assertFileSize(t, sessionTranscriptPath(handle.path, "a"), 0)
	assertFileSize(t, sessionTranscriptPath(handle.path, "b"), state.Transcript.Bytes)

	third := sessionStoreTestTerminalBlocks(t, []map[string]any{
		{"kind": 0, "text": "prompt"},
		{"kind": 1, "text": strings.Repeat("second ", 200)},
		{"kind": 1, "text": strings.Repeat("third ", 500)},
	})
	if err := handle.Save(agentCheckpoint, subagent.EmptyCheckpoint(), third, false, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	state, err = readSessionState(sessionStatePath(handle.path))
	if err != nil {
		t.Fatal(err)
	}
	if state.Transcript.Slot != "a" || state.Transcript.DeltaBytes != 0 || state.Transcript.BlockCount != 3 {
		t.Fatalf("second compacted transcript head = %+v", state.Transcript)
	}
	assertFileSize(t, sessionTranscriptPath(handle.path, "a"), state.Transcript.Bytes)
	assertFileSize(t, sessionTranscriptPath(handle.path, "b"), 0)

	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	encoded, err := json.Marshal(reopened.record.Terminal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "second") || !strings.Contains(string(encoded), "third") {
		t.Fatalf("restored compacted transcript = %s", encoded)
	}
}

func TestSessionStoreIgnoresInterruptedTranscriptCompaction(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "model", balanced: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := handle.path
	id := handle.record.ID
	state, err := readSessionState(sessionStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	inactivePath := sessionTranscriptPath(path, inactiveTranscriptSlot(state.Transcript.Slot))
	if err := os.WriteFile(inactivePath, []byte("partial compacted transcript"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.record.Terminal.Description() != "prompt" {
		t.Fatalf("restored description = %q", reopened.record.Terminal.Description())
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileSize(t, inactivePath, 0)
}

func TestSessionStoreIgnoresUncommittedTranscriptTail(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "model", balanced: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := handle.path
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := readSessionState(sessionStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := sessionTranscriptPath(path, state.Transcript.Slot)
	file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"replace_from":999}` + "\npartial"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != state.Transcript.Bytes {
		t.Fatalf("transcript size = %d, want %d", info.Size(), state.Transcript.Bytes)
	}
}

func TestSessionStoreRejectsShortCommittedTranscript(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "model", balanced: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := handle.path
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := readSessionState(sessionStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := sessionTranscriptPath(path, state.Transcript.Slot)
	if err := os.Truncate(transcriptPath, state.Transcript.Bytes-1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), cwd, id); err == nil {
		t.Fatal("short committed transcript was accepted")
	}
}

func TestSessionStoreListsMetadataWithoutDecodingCheckpoints(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "model", balanced: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := sessionStatePath(handle.path)
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"agent":{`, `"agent":"invalid","unused":{`, 1))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, warnings, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(summaries) != 1 || summaries[0].ID != id || summaries[0].Description != "prompt" {
		t.Fatalf("summaries = %+v, warnings = %v", summaries, warnings)
	}
	if _, err := store.Open(context.Background(), cwd, id); err == nil {
		t.Fatal("full session load accepted invalid checkpoint data")
	}
}

func assertFileSize(t *testing.T, path string, want int64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != want {
		t.Fatalf("%s size = %d, want %d", path, info.Size(), want)
	}
}

func TestSessionStoreRejectsWorldReadableAndCorruptRecords(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	store := newSessionStore(home)
	handle, err := store.Create("test", cwd, modelSet{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := sessionStatePath(handle.path)
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), cwd, id); err == nil {
		t.Fatalf("permission error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), cwd, id); err == nil {
		t.Fatal("corrupt session was accepted")
	}
	if _, err := store.Open(context.Background(), cwd, ""); err == nil {
		t.Fatalf("most recent corrupt-only error = %v", err)
	}

	valid, err := store.Create("test", cwd, modelSet{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "valid prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	validID := valid.record.ID
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	summaries, warnings, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != validID {
		t.Fatalf("summaries with corrupt record = %+v", summaries)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], filepath.ToSlash(filepath.Dir(path))) {
		t.Fatalf("warnings = %v", warnings)
	}

	mostRecent, err := store.Open(context.Background(), cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(mostRecent.warnings) != 1 || mostRecent.warnings[0] != warnings[0] {
		t.Fatalf("most recent warnings = %v", mostRecent.warnings)
	}
	if err := mostRecent.Close(); err != nil {
		t.Fatal(err)
	}
}
