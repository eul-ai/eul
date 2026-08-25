package terminal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckpointPersistenceRoundTrip(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurn("first prompt")
	model.appendBlock(blockAssistant, "first answer")
	model.history = []string{"older prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	checkpoint := checkpointModel(model, nil)

	transcript, state, err := SplitCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedState), "first prompt") || strings.Contains(string(encodedState), "first answer") {
		t.Fatalf("checkpoint state contains transcript: %s", encodedState)
	}
	var decodedState CheckpointState
	if err := json.Unmarshal(encodedState, &decodedState); err != nil {
		t.Fatal(err)
	}

	restored, err := JoinCheckpoint(transcript, decodedState)
	if err != nil {
		t.Fatal(err)
	}
	encodedCheckpoint, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	encodedRestored, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalCheckpointSemanticJSON(t, encodedRestored, encodedCheckpoint)
}

func TestTranscriptDeltaAppendsOnlyNewSuffix(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurn("prompt")
	previous, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}

	model.appendBlock(blockAssistant, "answer")
	next, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}
	delta, changed := DiffTranscript(previous, next)
	if !changed || delta.replaceFrom != 1 || len(delta.blocks) != 1 || delta.blocks[0].Text != "answer" {
		t.Fatalf("delta = %+v, changed = %v", delta, changed)
	}

	applied, err := ApplyTranscriptDelta(previous, delta)
	if err != nil {
		t.Fatal(err)
	}
	assertTranscriptsEqual(t, applied, next)
}

func TestTranscriptDeltaReplacesChangedSuffix(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurn("prompt")
	model.appendBlock(blockAssistant, "first")
	model.appendBlock(blockInfo, "tail")
	previous, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}

	model.blocks[1].text = "updated"
	next, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}
	delta, changed := DiffTranscript(previous, next)
	if !changed || delta.replaceFrom != 1 || len(delta.blocks) != 2 || delta.blocks[0].Text != "updated" || delta.blocks[1].Text != "tail" {
		t.Fatalf("delta = %+v, changed = %v", delta, changed)
	}

	encoded, err := json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TranscriptDelta
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyTranscriptDelta(previous, decoded)
	if err != nil {
		t.Fatal(err)
	}
	assertTranscriptsEqual(t, applied, next)
}

func TestTranscriptDeltaTruncatesAndClears(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurn("prompt")
	model.appendBlock(blockAssistant, "answer")
	previous, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}

	model.blocks = model.blocks[:1]
	truncated, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}
	delta, changed := DiffTranscript(previous, truncated)
	if !changed || delta.replaceFrom != 1 || len(delta.blocks) != 0 {
		t.Fatalf("truncate delta = %+v, changed = %v", delta, changed)
	}
	applied, err := ApplyTranscriptDelta(previous, delta)
	if err != nil {
		t.Fatal(err)
	}
	assertTranscriptsEqual(t, applied, truncated)

	cleared := EmptyTranscript()
	delta, changed = DiffTranscript(previous, cleared)
	if !changed || delta.replaceFrom != 0 || len(delta.blocks) != 0 {
		t.Fatalf("clear delta = %+v, changed = %v", delta, changed)
	}
	applied, err = ApplyTranscriptDelta(previous, delta)
	if err != nil {
		t.Fatal(err)
	}
	assertTranscriptsEqual(t, applied, cleared)
}

func TestTranscriptDeltaDetectsNoChange(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurn("prompt")
	transcript, _, err := SplitCheckpoint(checkpointModel(model, nil))
	if err != nil {
		t.Fatal(err)
	}
	if delta, changed := DiffTranscript(transcript, transcript); changed {
		t.Fatalf("unchanged delta = %+v", delta)
	}
}

func TestTranscriptDeltaRejectsMalformedData(t *testing.T) {
	for _, encoded := range []string{
		`{"replace_from":-1}`,
		`{"replace_from":0,"unknown":true}`,
		`{"replace_from":0}{"replace_from":0}`,
	} {
		var delta TranscriptDelta
		if err := json.Unmarshal([]byte(encoded), &delta); err == nil {
			t.Fatalf("malformed delta accepted: %s", encoded)
		}
	}

	if _, err := ApplyTranscriptDelta(EmptyTranscript(), TranscriptDelta{replaceFrom: 1}); err == nil {
		t.Fatal("out-of-range delta was accepted")
	}
}

func assertTranscriptsEqual(t *testing.T, got, want Transcript) {
	t.Helper()
	if got.BlockCount() != want.BlockCount() {
		t.Fatalf("block count = %d, want %d", got.BlockCount(), want.BlockCount())
	}
	for index := range got.blocks {
		if !checkpointBlocksEqual(got.blocks[index], want.blocks[index]) {
			t.Fatalf("block %d = %+v, want %+v", index, got.blocks[index], want.blocks[index])
		}
	}
}
