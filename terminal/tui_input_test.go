package terminal

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestKeyDecoderHandlesTextAndSplitSequences(t *testing.T) {
	decoder := &keyDecoder{}
	events := decoder.feed([]byte("hé\x1b["), false)
	events = append(events, decoder.feed([]byte("D\x7f\r"), false)...)

	want := []keyEvent{
		{code: keyText, text: "hé"},
		{code: keyLeft},
		{code: keyBackspace},
		{code: keyEnter},
	}
	assertKeyEvents(t, events, want)
}

func TestKeyDecoderRecognizesTabAndEscape(t *testing.T) {
	decoder := &keyDecoder{}
	assertKeyEvents(t, decoder.feed([]byte("\t\x1b[27u"), false), []keyEvent{{code: keyTab}, {code: keyEscape}})

	decoder = &keyDecoder{}
	if events := decoder.feed([]byte("\x1b"), false); len(events) != 0 {
		t.Fatalf("partial escape events = %+v", events)
	}
	assertKeyEvents(t, decoder.flushPendingEscape(), []keyEvent{{code: keyEscape}})

	decoder = &keyDecoder{}
	if events := decoder.feed([]byte("\x1b["), false); len(events) != 0 {
		t.Fatalf("partial arrow events = %+v", events)
	}
	if events := decoder.flushPendingEscape(); len(events) != 0 {
		t.Fatalf("timed-out partial arrow events = %+v", events)
	}
	assertKeyEvents(t, decoder.feed([]byte("A"), false), []keyEvent{{code: keyUp}})
}

func TestKeyDecoderHandlesModifiedKeys(t *testing.T) {
	for sequence, code := range map[string]keyCode{
		"\x1b[13;2u":      keyNewline,
		"\x1b[13;2:1u":    keyNewline,
		"\x1b[13:13;2:1u": keyNewline,
		"\x1b[13;2~":      keyNewline,
		"\x1b[27;2;13~":   keyNewline,
		"\x1b\r":          keyNewline,
		"\n":              keyNewline,
		"\x1b[13u":        keyEnter,
		"\x1b[57414u":     keyEnter,
		"\x1b[57414;1:1u": keyEnter,
		"\x1bOM":          keyEnter,
		"\x1b[57414;2u":   keyNewline,
		"\x1b[57414;2:1u": keyNewline,
		"\x1b[Z":          keyShiftTab,
		"\x1b[9;2u":       keyShiftTab,
		"\x1b[9;2:1u":     keyShiftTab,
		"\x1b[27;2;9~":    keyShiftTab,
		"\x01":            keyHome,
		"\x1b[97;5u":      keyHome,
		"\x1b[1u":         keyHome,
		"\x05":            keyEnd,
		"\x1b[101;5u":     keyEnd,
		"\x1b[5u":         keyEnd,
		"\x1b[99;5u":      keyCtrlC,
		"\x1b[99;69:1u":   keyCtrlC,
		"\x1b[3u":         keyCtrlC,
		"\x1b[100;5u":     keyCtrlD,
		"\x1b[108;5u":     keyCtrlL,
		"\x16":            keyCtrlV,
		"\x1b[118;5u":     keyCtrlV,
		"\x1b[118;9u":     keyCtrlV,
		"\x1b[127u":       keyBackspace,
		"\x1b[1;1:1D":     keyLeft,
		"\x1b[1;1:1C":     keyRight,
		"\x1b[1;1:1A":     keyUp,
		"\x1b[1;3A":       keyAltUp,
		"\x1b[1;3:1A":     keyAltUp,
		"\x1b[1;1:1B":     keyDown,
		"\x1b[1;1:1H":     keyHome,
		"\x1b[1;1:1F":     keyEnd,
		"\x1b[3;1:1~":     keyDelete,
		"\x1b[5;1:1~":     keyPageUp,
		"\x1b[6;1:1~":     keyPageDown,
	} {
		decoder := &keyDecoder{}
		events := decoder.feed([]byte(sequence), false)
		assertKeyEvents(t, events, []keyEvent{{code: code}})
	}
}

func TestKeyDecoderTreatsShiftSpaceAsSpace(t *testing.T) {
	decoder := &keyDecoder{}
	events := decoder.feed([]byte("\x1b[32;2u"), false)
	assertKeyEvents(t, events, []keyEvent{{code: keyText, text: " "}})
}

func TestKeyDecoderHandlesSGRMouseEvents(t *testing.T) {
	decoder := &keyDecoder{}
	if events := decoder.feed([]byte("\x1b[<0;10"), false); len(events) != 0 {
		t.Fatalf("partial mouse events = %+v", events)
	}
	events := decoder.feed([]byte(";5M\x1b[<32;12;7M\x1b[<0;12;7m\x1b[<64;3;4M\x1b[<65;3;4M"), false)
	assertKeyEvents(t, events, []keyEvent{
		{code: keyMouse, mouse: mouseEvent{kind: mousePress, column: 9, row: 4}},
		{code: keyMouse, mouse: mouseEvent{kind: mouseDrag, column: 11, row: 6}},
		{code: keyMouse, mouse: mouseEvent{kind: mouseRelease, column: 11, row: 6}},
		{code: keyMouse, mouse: mouseEvent{kind: mouseWheelUp, column: 2, row: 3}},
		{code: keyMouse, mouse: mouseEvent{kind: mouseWheelDown, column: 2, row: 3}},
	})

	if events := decoder.feed([]byte("\x1b[<2;1;1M\x1b[<2;1;1m"), false); len(events) != 0 {
		t.Fatalf("secondary-button events = %+v", events)
	}

	decoder = &keyDecoder{}
	if events := decoder.feed([]byte("\x1b[M`#"), false); len(events) != 0 {
		t.Fatalf("partial X10 mouse events = %+v", events)
	}
	assertKeyEvents(t, decoder.feed([]byte("$"), false), []keyEvent{
		{code: keyMouse, mouse: mouseEvent{kind: mouseWheelUp, column: 2, row: 3}},
	})
}

func TestKeyDecoderHandlesSplitKittySequenceAndIgnoresRelease(t *testing.T) {
	decoder := &keyDecoder{}
	if events := decoder.feed([]byte("\x1b[13;2:"), false); len(events) != 0 {
		t.Fatalf("partial events = %+v", events)
	}
	events := decoder.feed([]byte("1u\x1b[99;5:3u\x1b[?7u"), false)
	assertKeyEvents(t, events, []keyEvent{{code: keyNewline}})
}

func TestKeyDecoderHandlesBracketedPaste(t *testing.T) {
	decoder := &keyDecoder{}
	if events := decoder.feed([]byte("\x1b[200~first\n"), false); len(events) != 0 {
		t.Fatalf("initial events = %+v", events)
	}
	events := decoder.feed([]byte("second\x1b[201~x"), false)

	assertKeyEvents(t, events, []keyEvent{{code: keyText, text: "first\nsecondx"}})
}

func TestKeyDecoderIgnoresUnknownEscapeSequence(t *testing.T) {
	decoder := &keyDecoder{}
	events := decoder.feed([]byte("a\x1b[31mb"), false)
	assertKeyEvents(t, events, []keyEvent{{code: keyText, text: "ab"}})
}

func TestKeyDecoderRejectsInvalidAndOversizedPaste(t *testing.T) {
	decoder := &keyDecoder{}
	events := decoder.feed([]byte{0xff, 0}, true)
	if len(events) != 2 || !errors.Is(events[0].err, errInvalidInput) || !errors.Is(events[1].err, errInvalidInput) {
		t.Fatalf("invalid events = %+v", events)
	}

	decoder = &keyDecoder{}
	content := "\x1b[200~" + strings.Repeat("x", maxInputBytes+1) + "\x1b[201~"
	events = decoder.feed([]byte(content), false)
	if len(events) != 1 || !errors.Is(events[0].err, errInputTooLong) {
		t.Fatalf("oversized paste events = %+v", events)
	}
}

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
	if got := string(model.input); got != "a界" {
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
	if got := string(model.input); got != "a界" {
		t.Fatalf("history up = %q", got)
	}
	model.historyDown()
	if got := string(model.input); got != "draft" {
		t.Fatalf("history down = %q", got)
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

func TestArrowUpMovesToPreviousInputLineBeforeHistory(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.history = []string{"old prompt"}
	if err := model.insertInput("first\nsecond"); err != nil {
		t.Fatal(err)
	}

	if _, err := reduceKey(model, keyEvent{code: keyUp}); err != nil {
		t.Fatal(err)
	}
	if got := string(model.input); got != "first\nsecond" || model.cursor != len([]rune("first")) || model.historyIndex != -1 {
		t.Fatalf("first up: input=%q cursor=%d historyIndex=%d", got, model.cursor, model.historyIndex)
	}

	if _, err := reduceKey(model, keyEvent{code: keyUp}); err != nil {
		t.Fatal(err)
	}
	if got := string(model.input); got != "old prompt" || model.historyIndex != 0 {
		t.Fatalf("second up: input=%q historyIndex=%d", got, model.historyIndex)
	}
}

func TestArrowDownMovesToNextInputLineBeforeHistory(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.history = []string{"first\nsecond"}
	model.historyDraft = "draft"
	model.historyIndex = 0
	model.setInput(model.history[0])
	model.cursor = len([]rune("first"))

	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if got := string(model.input); got != "first\nsecond" || model.cursor != len([]rune("first\nsecon")) || model.historyIndex != 0 {
		t.Fatalf("first down: input=%q cursor=%d historyIndex=%d", got, model.cursor, model.historyIndex)
	}

	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if got := string(model.input); got != "draft" || model.historyIndex != -1 {
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
		ThinkingLevel:    agent.ThinkingHigh,
		ThinkingLevels:   []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh},
		SetThinkingLevel: setThinkingLevel,
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
		ThinkingLevel:    agent.ThinkingMedium,
		SetThinkingLevel: setThinkingLevel,
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

func handleModelKey(model *tuiModel, key keyEvent, setThinkingLevel func(agent.ThinkingLevel) error) (bool, error) {
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: messages, stopped: stopped, setThinkingLevel: setThinkingLevel,
	}
	return controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: key})
}

func TestTUIModelPreservesPastedNewlinesAndRejectsNUL(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("one\r\n\r\ntwo\tthree\x1b"); err != nil {
		t.Fatal(err)
	}
	if got := string(model.input); got != "one\n\ntwo three�" {
		t.Fatalf("input = %q", got)
	}
	if err := model.insertInput("bad\x00"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("insertInput() error = %v", err)
	}
}

func assertKeyEvents(t *testing.T, got, want []keyEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index].code != want[index].code || got[index].text != want[index].text || got[index].mouse != want[index].mouse || !errors.Is(got[index].err, want[index].err) {
			t.Fatalf("event %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
