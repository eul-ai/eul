package terminal

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
)

func TestDraftRoundTrip(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	draft := model.draft()
	draft.QueuedInputs = []string{"accepted", "deferred"}

	encoded, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var decoded session.Draft
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	conversation := newFakeSession()
	conversation.transcript.AppendUser(textContent("prompt"))
	conversation.info = session.Info{Draft: decoded, ContextTokens: 42, History: []string{"global prompt"}}
	restored := newTestModel(100, 30, conversation)
	if restored.running || restored.activity.kind != activityReady {
		t.Fatalf("runtime state was restored: %+v", restored)
	}
	if len(restored.entries) != 1 || restored.entries[0].Kind != session.EntryUser {
		t.Fatalf("entries = %+v", restored.entries)
	}
	if restored.inputText() != "accepted\n\ndeferred\n\ndraft" || restored.cursor != len(restored.input) {
		t.Fatalf("input=%q cursor=%d", restored.inputText(), restored.cursor)
	}
	if !slices.Equal(restored.globalHistory, []string{"global prompt"}) || len(restored.history) != 0 {
		t.Fatalf("global history = %q, history = %q", restored.globalHistory, restored.history)
	}
	if restored.contextTokens != 42 {
		t.Fatalf("context tokens = %d", restored.contextTokens)
	}
}

func TestDraftProjectsImagesOut(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("before "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	if err := model.insertInput("after"); err != nil {
		t.Fatal(err)
	}
	model.moveLeft()

	draft := model.draft()
	if draft.Input != "before after" || draft.Cursor != len([]rune("before afte")) {
		t.Fatalf("draft = %+v", draft)
	}
	encoded, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cG5n") {
		t.Fatalf("draft image was persisted: %s", encoded)
	}
}

func TestDraftRestoreClampsCursor(t *testing.T) {
	model := newTestModel(80, 24, &fakeSession{info: session.Info{Draft: session.Draft{Input: "ab", Cursor: 9}}})
	if model.inputText() != "ab" || model.cursor != 2 {
		t.Fatalf("input = %q, cursor = %d", model.inputText(), model.cursor)
	}
}
