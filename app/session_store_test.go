package app

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
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
	agentCheckpoint := sessionStoreTestAgentCheckpoint(t)

	first, err := store.Create("test", cwd, backend.Models{Main: "model", Fast: "fast-model", Balanced: "balanced-model"}, agent.ThinkingHigh, agentCheckpoint, subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "first prompt\nmore"), true)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.record.ID
	if first.record.Provider != "test" || first.record.Models.Fast != "fast-model" || first.record.Models.Balanced != "balanced-model" || !first.record.FastMode {
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

	second, err := store.Create("test", cwd, backend.Models{Main: "model", Fast: "fast-model", Balanced: "balanced-model"}, agent.ThinkingMedium, agentCheckpoint, subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "second prompt"), false)
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
	for _, name := range []string{sessionMetadataFileName, sessionStateFileName, sessionLockFileName} {
		fileInfo, err := os.Stat(filepath.Join(sessionDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %o", name, fileInfo.Mode().Perm())
		}
	}

	locked, err := store.Open(cwd, secondID)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != firstID || summaries[0].Description != "first prompt" || !summaries[0].Active {
		t.Fatalf("summaries = %+v", summaries)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}

	mostRecent, err := store.Open(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if mostRecent.record.ID != secondID {
		t.Fatalf("most recent ID = %q", mostRecent.record.ID)
	}
	if _, err := store.Open(cwd, secondID); !errors.Is(err, errSessionInUse) {
		t.Fatalf("lock contention error = %v", err)
	}
	if err := mostRecent.Close(); err != nil {
		t.Fatal(err)
	}

	otherCWD := t.TempDir()
	explicit, err := store.Open(otherCWD, firstID)
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

func TestSessionMetadataStoresModelsAsJSONObject(t *testing.T) {
	models := backend.Models{Main: "main-model", Fast: "fast-model", Balanced: "balanced-model"}
	encoded, err := json.Marshal(sessionMetadata{Models: models})
	if err != nil {
		t.Fatal(err)
	}

	var fields struct {
		Models map[string]string `json:"models"`
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"main": "main-model", "fast": "fast-model", "balanced": "balanced-model"}
	if !maps.Equal(fields.Models, want) {
		t.Fatalf("models = %v, want %v", fields.Models, want)
	}
}

func TestSessionStoreIgnoresLegacySessionFiles(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	workspace := store.workspaceDirectory(cwd)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(workspace, id+".json"), []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("summaries = %+v", summaries)
	}
	if _, err := store.Open(cwd, id); err == nil {
		t.Fatal("legacy session file was opened")
	}
}

func TestSessionStoreIgnoresAbandonedEmptyDirectory(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	workspace := store.workspaceDirectory(cwd)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	if err := os.Mkdir(filepath.Join(workspace, id), 0o700); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("summaries = %+v", summaries)
	}
	if _, err := store.Open(cwd, id); err == nil {
		t.Fatal("abandoned empty session directory was opened")
	}
}

func TestSessionStoreDoesNotPersistOrListEmptySessions(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, backend.Models{Main: "model", Fast: "fast-model", Balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), terminal.EmptyCheckpoint(), false)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(handle.path, sessionLockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := handle.Save(sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), terminal.EmptyCheckpoint(), false, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{sessionMetadataFileName, sessionStateFileName} {
		if _, err := os.Stat(filepath.Join(handle.path, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty session %s error = %v", name, err)
		}
	}
	summaries, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(handle.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty session directory error = %v", err)
	}
}

func TestSessionStoreListsMetadataWithoutDecodingCheckpoints(t *testing.T) {
	store := newSessionStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create("test", cwd, backend.Models{Main: "model", Fast: "model", Balanced: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(handle.path, sessionStateFileName)
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"agent":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != id || summaries[0].Description != "prompt" {
		t.Fatalf("summaries = %+v", summaries)
	}
	if _, err := store.Open(cwd, id); err == nil {
		t.Fatal("full session load accepted invalid checkpoint data")
	}
}

func TestSessionStoreSkipsCorruptSessions(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	store := newSessionStore(home)
	handle, err := store.Create("test", cwd, backend.Models{Main: "model", Fast: "fast-model", Balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(handle.path, sessionMetadataFileName)
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(`{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(cwd, id); err == nil {
		t.Fatal("corrupt session was accepted")
	}
	if _, err := store.Open(cwd, ""); err == nil {
		t.Fatalf("most recent corrupt-only error = %v", err)
	}

	valid, err := store.Create("test", cwd, backend.Models{Main: "model", Fast: "fast-model", Balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "valid prompt"), false)
	if err != nil {
		t.Fatal(err)
	}
	validID := valid.record.ID
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	summaries, err := store.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != validID {
		t.Fatalf("summaries with corrupt record = %+v", summaries)
	}

	mostRecent, err := store.Open(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if mostRecent.record.ID != validID {
		t.Fatalf("most recent ID = %q", mostRecent.record.ID)
	}
	if err := mostRecent.Close(); err != nil {
		t.Fatal(err)
	}
}
