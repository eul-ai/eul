package terminal

import (
	"context"
	"errors"
	"io"
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

func handleModelKey(model *tuiModel, key keyEvent, setThinkingLevel func(agent.ThinkingLevel) error) (bool, error) {
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, operations: operationsFor(&fakeEngine{}), controls: Controls{SetThinkingLevel: setThinkingLevel}, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	return controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: key})
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
