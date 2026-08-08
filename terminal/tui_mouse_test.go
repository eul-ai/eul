package terminal

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMouseWheelScrollsConversationWithoutNavigatingHistory(t *testing.T) {
	model := newTUIModel(24, 10, Options{})
	model.appendBlock(blockAssistant, strings.Join([]string{
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
	}, "\n"))
	model.history = []string{"old prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	buildTerminalFrame(model)
	model.running = true
	bottom := model.scrollTop
	if bottom == 0 {
		t.Fatal("conversation did not overflow the viewport")
	}

	decoder := &keyDecoder{}
	events := decoder.feed([]byte("\x1b[<64;2;2M"), false)
	if len(events) != 1 {
		t.Fatalf("wheel events = %+v", events)
	}
	if exit, err := handleModelKey(model, events[0]); err != nil || exit {
		t.Fatalf("handle wheel: exit=%t err=%v", exit, err)
	}
	if model.scrollTop != max(0, bottom-mouseWheelScrollLines) || model.following {
		t.Fatalf("scrolled conversation: top=%d bottom=%d following=%t", model.scrollTop, bottom, model.following)
	}
	if got := string(model.input); got != "draft" || model.historyIndex != -1 {
		t.Fatalf("mouse wheel navigated input history: input=%q historyIndex=%d", got, model.historyIndex)
	}

	if exit, err := handleModelKey(model, keyEvent{code: keyMouse, mouse: mouseEvent{kind: mouseWheelDown}}); err != nil || exit {
		t.Fatalf("handle wheel down: exit=%t err=%v", exit, err)
	}
	if model.scrollTop != bottom || !model.following {
		t.Fatalf("conversation did not return to bottom: top=%d bottom=%d following=%t", model.scrollTop, bottom, model.following)
	}
}

func TestMouseDragSelectsAndCopiesRenderedText(t *testing.T) {
	model := newTUIModel(20, 10, Options{})
	model.appendBlock(blockAssistant, "alpha beta")
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	buildTerminalFrame(model)

	var output bytes.Buffer
	for _, event := range []mouseEvent{
		{kind: mousePress, column: 1, row: 1},
		{kind: mouseDrag, column: 5, row: 1},
	} {
		if exit, err := handleModelMouse(model, &output, event); err != nil || exit {
			t.Fatalf("handle mouse %+v: exit=%t err=%v", event, exit, err)
		}
	}
	if row := buildTerminalFrame(model).rows[1]; !strings.Contains(row, ansiReverse+"alpha") {
		t.Fatalf("selected row does not highlight text: %q", row)
	}

	if exit, err := handleModelMouse(model, &output, mouseEvent{kind: mouseRelease, column: 5, row: 1}); err != nil || exit {
		t.Fatalf("release selection: exit=%t err=%v", exit, err)
	}
	if got, want := output.String(), "\x1b]52;c;YWxwaGE=\x07"; got != want {
		t.Fatalf("clipboard output = %q, want %q", got, want)
	}
	if got := string(model.input); got != "draft" || model.cursor != len([]rune("draft")) {
		t.Fatalf("selection changed input: input=%q cursor=%d", got, model.cursor)
	}
}

func TestMouseClickDoesNotCopy(t *testing.T) {
	model := newTUIModel(20, 10, Options{})
	model.appendBlock(blockAssistant, "alpha")
	buildTerminalFrame(model)

	var output bytes.Buffer
	if _, err := handleModelMouse(model, &output, mouseEvent{kind: mousePress, column: 1, row: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := handleModelMouse(model, &output, mouseEvent{kind: mouseRelease, column: 1, row: 1}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || model.selection.set {
		t.Fatalf("click copied or retained a selection: output=%q selection=%+v", output.String(), model.selection)
	}
}

func TestSelectedTextSnapsToWideCharacters(t *testing.T) {
	bounds := selectionBounds{
		start: selectionPoint{row: 0, column: 1},
		end:   selectionPoint{row: 0, column: 2},
	}
	if got := selectedTextFromLines([]string{"a界b"}, bounds); got != "界" {
		t.Fatalf("selected text = %q, want %q", got, "界")
	}
}

func TestFullScreenModeEnablesAndDisablesMouseReporting(t *testing.T) {
	for _, sequence := range []string{"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1006h"} {
		if !strings.Contains(enterScreen, sequence) {
			t.Fatalf("enter screen is missing %q", sequence)
		}
	}
	for _, sequence := range []string{"\x1b[?1006l", "\x1b[?1002l", "\x1b[?1000l"} {
		if !strings.Contains(leaveScreen, sequence) {
			t.Fatalf("leave screen is missing %q", sequence)
		}
	}
}

func handleModelMouse(model *tuiModel, output *bytes.Buffer, mouse mouseEvent) (bool, error) {
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc
	return handleKeyWithOutput(
		context.Background(),
		model,
		&fakeEngine{},
		output,
		keyEvent{code: keyMouse, mouse: mouse},
		messages,
		stopped,
		&cancel,
	)
}
