package terminal

import (
	"errors"
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestTUIModelEditsAndNavigatesHistory(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("ab"); err != nil {
		t.Fatal(err)
	}
	model.moveLeft()
	if err := model.insertInput("界"); err != nil {
		t.Fatal(err)
	}
	model.delete()
	if got := model.inputText(); got != "a界" {
		t.Fatalf("input = %q", got)
	}

	prompt, ok := model.takePrompt()
	if !ok || prompt != "a界" {
		t.Fatalf("takePrompt() = %q, %v", prompt, ok)
	}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	model.historyUp()
	if got := model.inputText(); got != "a界" {
		t.Fatalf("history up = %q", got)
	}
	model.historyDown()
	if got := model.inputText(); got != "draft" {
		t.Fatalf("history down = %q", got)
	}
}

func TestTUIModelEditsInlineImages(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("before after"); err != nil {
		t.Fatal(err)
	}
	model.cursor = len(editorItemsFromText("before "))
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if model.cursor != len(editorItemsFromText("before "))+1 {
		t.Fatalf("cursor = %d", model.cursor)
	}

	content := editorContent(model.input)
	if len(content) != 3 || content[0].Text != "before " || content[1].Image == nil || string(content[1].Image.Data) != "one" || content[2].Text != "after" {
		t.Fatalf("content = %+v", content)
	}

	model.moveLeft()
	model.delete()
	if got := model.inputText(); got != "before after" || model.imageCount() != 0 {
		t.Fatalf("input = %q, images = %d", got, model.imageCount())
	}

	model.cursor = len(editorItemsFromText("before "))
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("two")}); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("three")}); err != nil {
		t.Fatal(err)
	}
	model.backspace()
	if model.imageCount() != 1 {
		t.Fatalf("images after first backspace = %d", model.imageCount())
	}
	model.backspace()
	if model.imageCount() != 0 || model.cursor != len(editorItemsFromText("before ")) {
		t.Fatalf("cursor = %d, images = %d", model.cursor, model.imageCount())
	}
}

func TestTUIModelInsertsNewlineAndPreservesItInPrompt(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("firstsecond"); err != nil {
		t.Fatal(err)
	}
	model.cursor = len([]rune("first"))
	if err := model.insertNewline(); err != nil {
		t.Fatal(err)
	}

	prompt, ok := model.takePrompt()
	if !ok || prompt != "first\nsecond" {
		t.Fatalf("takePrompt() = %q, %v", prompt, ok)
	}
}

func TestTUIModelNavigatesLinesWithImages(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("ab"); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	if err := model.insertInput("c\nxy"); err != nil {
		t.Fatal(err)
	}
	model.cursor = len(model.input)
	if !model.moveUp() || model.cursor != 2 {
		t.Fatalf("up cursor = %d", model.cursor)
	}
	model.moveEnd()
	if model.cursor != 4 {
		t.Fatalf("end cursor = %d", model.cursor)
	}
	model.moveHome()
	if model.cursor != 0 {
		t.Fatalf("home cursor = %d", model.cursor)
	}
	if !model.moveDown() || model.cursor != 5 {
		t.Fatalf("down cursor = %d", model.cursor)
	}
}

func TestTUIModelHistoryRestoresInlineDraft(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.history = []string{"old prompt"}
	if err := model.insertInput("draft "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	cursor := model.cursor

	model.historyUp()
	if got := model.inputText(); got != "old prompt" {
		t.Fatalf("history input = %q", got)
	}
	model.historyDown()
	if got := model.inputText(); got != "draft " || model.imageCount() != 1 || model.cursor != cursor {
		t.Fatalf("draft = %q, images = %d, cursor = %d", got, model.imageCount(), model.cursor)
	}
}

func TestArrowUpMovesToPreviousInputLineBeforeHistory(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.history = []string{"old prompt"}
	if err := model.insertInput("first\nsecond"); err != nil {
		t.Fatal(err)
	}

	if _, err := reduceKey(model, keyEvent{code: keyUp}); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "first\nsecond" || model.cursor != len([]rune("first")) || model.historyIndex != -1 {
		t.Fatalf("first up: input=%q cursor=%d historyIndex=%d", got, model.cursor, model.historyIndex)
	}

	if _, err := reduceKey(model, keyEvent{code: keyUp}); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "old prompt" || model.historyIndex != 0 {
		t.Fatalf("second up: input=%q historyIndex=%d", got, model.historyIndex)
	}
}

func TestArrowDownMovesToNextInputLineBeforeHistory(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.history = []string{"first\nsecond"}
	model.historyDraft = editorItemsFromText("draft")
	model.historyDraftCursor = len(model.historyDraft)
	model.historyIndex = 0
	model.setInput(model.history[0])
	model.cursor = len([]rune("first"))

	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "first\nsecond" || model.cursor != len([]rune("first\nsecon")) || model.historyIndex != 0 {
		t.Fatalf("first down: input=%q cursor=%d historyIndex=%d", got, model.cursor, model.historyIndex)
	}

	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "draft" || model.historyIndex != -1 {
		t.Fatalf("second down: input=%q historyIndex=%d", got, model.historyIndex)
	}
}

func TestHomeAndEndMoveWithinCurrentInputLine(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.setInput("first\nsecond\nthird")
	model.cursor = len([]rune("first\nsec"))

	if _, err := reduceKey(model, keyEvent{code: keyHome}); err != nil {
		t.Fatal(err)
	}
	if model.cursor != len([]rune("first\n")) {
		t.Fatalf("home cursor = %d", model.cursor)
	}

	if _, err := reduceKey(model, keyEvent{code: keyEnd}); err != nil {
		t.Fatal(err)
	}
	if model.cursor != len([]rune("first\nsecond")) {
		t.Fatalf("end cursor = %d", model.cursor)
	}
}

func TestTUIModelDefaultsToMediumThinking(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if model.thinkingLevel != agent.DefaultThinkingLevel {
		t.Fatalf("thinking level = %q", model.thinkingLevel)
	}
}

func TestTUIModelCyclesSupportedThinkingLevels(t *testing.T) {
	var configured []agent.ThinkingLevel
	setThinkingLevel := func(level agent.ThinkingLevel) error {
		configured = append(configured, level)
		return nil
	}
	model := newTUIModel(80, 24, Options{
		Config: Config{
			ThinkingLevel:  agent.ThinkingHigh,
			ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh},
		},
		Controls: Controls{SetThinkingLevel: setThinkingLevel},
	})
	for _, want := range []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh} {
		exit, err := handleModelKey(model, keyEvent{code: keyShiftTab}, setThinkingLevel)
		if err != nil || exit {
			t.Fatalf("handleKey() exit=%v error=%v", exit, err)
		}
		if model.thinkingLevel != want {
			t.Fatalf("thinking level = %q, want %q", model.thinkingLevel, want)
		}
	}
	wantConfigured := []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh}
	if !slices.Equal(configured, wantConfigured) {
		t.Fatalf("configured = %q, want %q", configured, wantConfigured)
	}
}

func TestTUIModelKeepsThinkingLevelWhenUpdateFails(t *testing.T) {
	setThinkingLevel := func(agent.ThinkingLevel) error { return errors.New("update failed") }
	model := newTUIModel(80, 24, Options{
		Config:   Config{ThinkingLevel: agent.ThinkingMedium},
		Controls: Controls{SetThinkingLevel: setThinkingLevel},
	})
	exit, err := handleModelKey(model, keyEvent{code: keyShiftTab}, setThinkingLevel)
	if err != nil || exit {
		t.Fatalf("handleKey() exit=%v error=%v", exit, err)
	}
	if model.thinkingLevel != agent.ThinkingMedium {
		t.Fatalf("thinking level = %q", model.thinkingLevel)
	}
	if model.activity.kind != activityError || model.activity.detail != "update failed" {
		t.Fatalf("activity = %+v", model.activity)
	}
}

func TestTUIModelPreservesPastedNewlinesAndRejectsNUL(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("one\r\n\r\ntwo\tthree\x1b"); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "one\n\ntwo three�" {
		t.Fatalf("input = %q", got)
	}
	if err := model.insertInput("bad\x00"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("insertInput() error = %v", err)
	}
}
