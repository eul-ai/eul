package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
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

	first, err := store.Create("test", cwd, modelSelection{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingHigh, agentCheckpoint, sessionStoreTestTerminalCheckpoint(t, "first prompt\nmore"), true)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.record.ID
	if first.record.Provider != "test" || first.record.FastModel != "fast-model" || first.record.BalancedModel != "balanced-model" || !first.record.FastMode {
		t.Fatalf("provider and models = %+v", first.record)
	}
	if err := first.Save(agentCheckpoint, first.record.Terminal, true, agent.ThinkingHigh, false); err != nil {
		t.Fatal(err)
	}
	if first.record.FastMode {
		t.Fatalf("fast mode was not updated: %+v", first.record)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	second, err := store.Create("test", cwd, modelSelection{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, agentCheckpoint, sessionStoreTestTerminalCheckpoint(t, "second prompt"), false)
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
	fileInfo, err := os.Stat(filepath.Join(workspace, firstID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("session permissions = %o", fileInfo.Mode().Perm())
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
	if _, err := store.Open(context.Background(), cwd, secondID); err == nil || !strings.Contains(err.Error(), "already in use") {
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

func TestVersionOneSessionFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/session-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := readSessionRecord(path)
	if err != nil {
		t.Fatal(err)
	}

	wantModels := modelSelection{main: "main-model", fast: "fast-model", balanced: "balanced-model"}
	if record.Version != 1 || record.ID != "0123456789abcdef0123456789abcdef" || record.Revision != 7 {
		t.Fatalf("record identity = %+v", record)
	}
	if record.WorkingDirectory != "/workspace/eul" || record.models() != wantModels || record.ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("record configuration = %+v", record)
	}
	if !record.Agent.Initialized() || !record.Terminal.Initialized() || record.Terminal.Description() != "user" {
		t.Fatalf("record checkpoints = %+v", record)
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionSemanticJSON(t, encoded, fixture)
}

func assertSessionSemanticJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestSessionRecordModelSelectionKeepsVersionOneJSONFields(t *testing.T) {
	models := modelSelection{main: "main-model", fast: "fast-model", balanced: "balanced-model"}
	encoded, err := json.Marshal(sessionRecord{
		Version:       sessionRecordVersion,
		Model:         models.main,
		FastModel:     models.fast,
		BalancedModel: models.balanced,
		Agent:         sessionStoreTestAgentCheckpoint(t),
		Terminal:      terminal.EmptyCheckpoint(),
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
		t.Fatal("session record added a models JSON field")
	}

	var record sessionRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	if record.models() != models {
		t.Fatalf("round-trip models = %+v, want %+v", record.models(), models)
	}
}

func TestSessionStoreDoesNotPersistOrListEmptySessions(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSelection{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), terminal.EmptyCheckpoint(), false)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := sessionLockPath(handle.path)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty session file error = %v", err)
	}
	if err := handle.Save(sessionStoreTestAgentCheckpoint(t), terminal.EmptyCheckpoint(), false, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved empty session file error = %v", err)
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

func TestSessionStoreListsMetadataWithoutDecodingCheckpoints(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, modelSelection{main: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := handle.path
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"agent": {`, `"agent": "invalid", "unused": {`, 1))
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

func TestSessionStoreRejectsWorldReadableAndCorruptRecords(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	store := newSessionStore(home)
	handle, err := store.Create("test", cwd, modelSelection{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := handle.path
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), cwd, id); err == nil || !strings.Contains(err.Error(), "0600") {
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
	if _, err := store.Open(context.Background(), cwd, ""); err == nil || !strings.Contains(err.Error(), "Skipped session") || !strings.Contains(err.Error(), "unsupported session version") {
		t.Fatalf("most recent corrupt-only error = %v", err)
	}

	valid, err := store.Create("test", cwd, modelSelection{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), sessionStoreTestTerminalCheckpoint(t, "valid prompt"), false)
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
	if len(warnings) != 1 || !strings.Contains(warnings[0], filepath.ToSlash(path)) || !strings.Contains(warnings[0], "unsupported session version") {
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

func sessionStoreTestAgentCheckpoint(t testing.TB) agent.Checkpoint {
	t.Helper()
	var checkpoint agent.Checkpoint
	if err := json.Unmarshal([]byte(`{"version":1,"context_usage":{"InputTokens":0,"OutputTokens":0,"TotalTokens":0}}`), &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func sessionStoreTestTerminalCheckpoint(t testing.TB, prompt string) terminal.Checkpoint {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"version": 1,
		"blocks": []map[string]any{{
			"kind": 0,
			"text": prompt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint terminal.Checkpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
