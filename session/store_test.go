package session

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

func TestStorePartitionsListsAndResolvesSessions(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewStore(home)
	models := backend.Models{Main: "model", Fast: "fast-model", Balanced: "balanced-model"}

	first := createStoredSession(t, store, Settings{Provider: "test", WorkingDirectory: cwd, Models: models, ThinkingLevel: agent.ThinkingHigh, FastMode: true}, "first prompt\nmore")
	firstID := first.record.ID
	if first.record.Provider != "test" || first.record.Models.Fast != "fast-model" || first.record.Models.Balanced != "balanced-model" || !first.record.FastMode {
		t.Fatalf("provider and models = %+v", first.record)
	}
	if err := first.save(first.record.state, true, agent.ThinkingHigh, false); err != nil {
		t.Fatal(err)
	}
	if first.record.FastMode {
		t.Fatalf("fast mode was not updated: %+v", first.record)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := createStoredSession(t, store, Settings{Provider: "test", WorkingDirectory: cwd, Models: models, ThinkingLevel: agent.ThinkingMedium}, "second prompt")
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
	for _, name := range []string{metadataFileName, stateFileName, lockFileName} {
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
	summaries, err := store.list(cwd)
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
	if _, err := store.Open(cwd, secondID); !errors.Is(err, errInUse) {
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

func TestMetadataStoresModelsAsJSONObject(t *testing.T) {
	models := backend.Models{Main: "main-model", Fast: "fast-model", Balanced: "balanced-model"}
	encoded, err := json.Marshal(metadata{Settings: Settings{Models: models}})
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

func TestStoreIgnoresLegacySessionFiles(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	workspace := store.workspaceDirectory(cwd)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(workspace, id+".json"), []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.list(cwd)
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

func TestStoreIgnoresAbandonedEmptyDirectory(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	workspace := store.workspaceDirectory(cwd)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	if err := os.Mkdir(filepath.Join(workspace, id), 0o700); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.list(cwd)
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

func TestStoreDoesNotPersistOrListEmptySessions(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create(Settings{Provider: "test", WorkingDirectory: cwd, Models: backend.Models{Main: "model"}, ThinkingLevel: agent.ThinkingMedium})
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(handle.path, lockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := handle.save(state{}, false, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{metadataFileName, stateFileName} {
		if _, err := os.Stat(filepath.Join(handle.path, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty session %s error = %v", name, err)
		}
	}
	summaries, err := store.list(cwd)
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

func TestStoreListsMetadataWithoutDecodingCheckpoints(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	handle := createStoredSession(t, store, Settings{Provider: "test", WorkingDirectory: cwd, Models: backend.Models{Main: "model"}}, "prompt")
	path := filepath.Join(handle.path, stateFileName)
	id := handle.record.ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"agent":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.list(cwd)
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

func TestStoreSkipsCorruptSessions(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	store := NewStore(home)
	handle := createStoredSession(t, store, Settings{Provider: "test", WorkingDirectory: cwd, Models: backend.Models{Main: "model"}}, "prompt")
	path := filepath.Join(handle.path, metadataFileName)
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

	valid := createStoredSession(t, store, Settings{Provider: "test", WorkingDirectory: cwd, Models: backend.Models{Main: "model"}}, "valid prompt")
	validID := valid.record.ID
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	summaries, err := store.list(cwd)
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

func createStoredSession(t *testing.T, store *Store, settings Settings, prompt string) *Handle {
	t.Helper()
	handle, err := store.Create(settings)
	if err != nil {
		t.Fatal(err)
	}
	next := state{Transcript: []Entry{{Kind: EntryUser, Text: prompt}}}
	if err := handle.save(next, false, settings.ThinkingLevel, settings.FastMode); err != nil {
		t.Fatal(err)
	}
	return handle
}
