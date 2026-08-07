package terminal

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"yaah/agent"
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
		"\x1b[Z":          keyShiftTab,
		"\x1b[9;2u":       keyShiftTab,
		"\x1b[9;2:1u":     keyShiftTab,
		"\x1b[27;2;9~":    keyShiftTab,
		"\x1b[99;5u":      keyCtrlC,
		"\x1b[99;69:1u":   keyCtrlC,
		"\x1b[3u":         keyCtrlC,
		"\x1b[100;5u":     keyCtrlD,
		"\x1b[108;5u":     keyCtrlL,
		"\x1b[127u":       keyBackspace,
		"\x1b[1;1:1D":     keyLeft,
		"\x1b[1;1:1C":     keyRight,
		"\x1b[1;1:1A":     keyUp,
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

func TestTUIModelDefaultsToMediumThinking(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if model.thinkingLevel != agent.DefaultThinkingLevel {
		t.Fatalf("thinking level = %q", model.thinkingLevel)
	}
}

func TestTUIModelCyclesSupportedThinkingLevels(t *testing.T) {
	var configured []agent.ThinkingLevel
	model := newTUIModel(80, 24, Options{
		ThinkingLevel:  agent.ThinkingHigh,
		ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh},
		SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = append(configured, level)
			return nil
		},
	})
	for _, want := range []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh} {
		if err := model.cycleThinkingLevel(); err != nil {
			t.Fatal(err)
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
	model := newTUIModel(80, 24, Options{
		ThinkingLevel: agent.ThinkingMedium,
		SetThinkingLevel: func(agent.ThinkingLevel) error {
			return errors.New("update failed")
		},
	})
	if err := model.cycleThinkingLevel(); err == nil {
		t.Fatal("cycleThinkingLevel() succeeded")
	}
	if model.thinkingLevel != agent.ThinkingMedium {
		t.Fatalf("thinking level = %q", model.thinkingLevel)
	}
}

func TestTUIModelNormalizesPasteAndRejectsNUL(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("one\ntwo\tthree\x1b"); err != nil {
		t.Fatal(err)
	}
	if got := string(model.input); got != "one two three�" {
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
		if got[index].code != want[index].code || got[index].text != want[index].text || !errors.Is(got[index].err, want[index].err) {
			t.Fatalf("event %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
