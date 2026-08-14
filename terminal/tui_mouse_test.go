package terminal

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHighlightCellsIgnoresClickableLinkEscapes(t *testing.T) {
	open := "\x1b]8;;https://example.com\x1b\\"
	value := open + "link" + ansiLinkClose
	got := highlightCells(value, 0, 4)
	if !strings.Contains(got, open+ansiReverse+"link"+ansiLinkClose) {
		t.Fatalf("highlighted link = %q", got)
	}
}

func TestMouseWheelScrollsConversationWithoutNavigatingHistory(t *testing.T) {
	model := newTUIModel(24, 10, Options{})
	model.appendBlock(blockAssistant, strings.Join([]string{
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
	}, "\n"))
	model.history = []string{"old prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	frame := buildTerminalFrame(model)
	model.running = true
	bottom := frame.conversationTop
	model.scrollTop = bottom
	if bottom == 0 {
		t.Fatal("conversation did not overflow the viewport")
	}

	decoder := &keyDecoder{}
	events := decoder.feed([]byte("\x1b[<64;2;2M"), false)
	if len(events) != 1 {
		t.Fatalf("wheel events = %+v", events)
	}
	if action := reduceMouse(model, events[0].mouse, frame); action.kind != tuiActionNone {
		t.Fatalf("wheel action = %+v", action)
	}
	if model.scrollTop != max(0, bottom-mouseWheelScrollLines) || model.following {
		t.Fatalf("scrolled conversation: top=%d bottom=%d following=%t", model.scrollTop, bottom, model.following)
	}
	if got := model.inputText(); got != "draft" || model.historyIndex != -1 {
		t.Fatalf("mouse wheel navigated input history: input=%q historyIndex=%d", got, model.historyIndex)
	}

	if action := reduceMouse(model, mouseEvent{kind: mouseWheelDown}, frame); action.kind != tuiActionNone {
		t.Fatalf("wheel action = %+v", action)
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
	committed := buildTerminalFrame(model)

	var output bytes.Buffer
	for _, event := range []mouseEvent{
		{kind: mousePress, column: 1, row: 1},
		{kind: mouseDrag, column: 5, row: 1},
	} {
		if exit, err := handleModelMouse(model, &output, committed, event); err != nil || exit {
			t.Fatalf("handle mouse %+v: exit=%t err=%v", event, exit, err)
		}
	}
	if row := buildTerminalFrame(model).rows[1]; !strings.Contains(row, ansiReverse+"alpha") {
		t.Fatalf("selected row does not highlight text: %q", row)
	}

	if exit, err := handleModelMouse(model, &output, committed, mouseEvent{kind: mouseRelease, column: 5, row: 1}); err != nil || exit {
		t.Fatalf("release selection: exit=%t err=%v", exit, err)
	}
	if got, want := output.String(), "\x1b]52;c;YWxwaGE=\x07"; got != want {
		t.Fatalf("clipboard output = %q, want %q", got, want)
	}
	if model.selection.set {
		t.Fatalf("selection remains after copy: %+v", model.selection)
	}
	if got := model.inputText(); got != "draft" || model.cursor != len([]rune("draft")) {
		t.Fatalf("selection changed input: input=%q cursor=%d", got, model.cursor)
	}
}

func TestMouseSelectionDoesNotHighlightConversationPadding(t *testing.T) {
	model := newTUIModel(20, 10, Options{})
	model.appendBlock(blockAssistant, "alpha")
	committed := buildTerminalFrame(model)

	var output bytes.Buffer
	for _, event := range []mouseEvent{
		{kind: mousePress, column: 0, row: 1},
		{kind: mouseDrag, column: 15, row: 1},
	} {
		if exit, err := handleModelMouse(model, &output, committed, event); err != nil || exit {
			t.Fatalf("handle mouse %+v: exit=%t err=%v", event, exit, err)
		}
	}

	_, layout := modelInputLayout(model)
	frame := buildTerminalFrame(model)
	selection, ok := selectionForScreenRow(model, layout, 1, frame.plainRows[1])
	if !ok || selection != (cellRange{start: 1, end: 6}) {
		t.Fatalf("highlighted cells = %+v, ok=%t", selection, ok)
	}
	if !strings.Contains(frame.rows[1], ansiReverse+"alpha"+ansiNotReverse) {
		t.Fatalf("selected row highlights padding: %q", frame.rows[1])
	}

	if exit, err := handleModelMouse(model, &output, committed, mouseEvent{kind: mouseRelease, column: 15, row: 1}); err != nil || exit {
		t.Fatalf("release selection: exit=%t err=%v", exit, err)
	}
	if got, want := output.String(), "\x1b]52;c;YWxwaGE=\x07"; got != want {
		t.Fatalf("clipboard output = %q, want %q", got, want)
	}
}

func TestMouseSelectionOmitsConversationPaddingAndWrappedNewlines(t *testing.T) {
	model := newTUIModel(8, 10, Options{})
	model.appendBlock(blockAssistant, "abcdefghij")
	frame := buildTerminalFrame(model)
	bounds := selectionBounds{
		start: selectionPoint{row: 1, column: 0, conversation: true},
		end:   selectionPoint{row: 2, column: 7, conversation: true},
	}

	if got := selectedConversationText(frame.conversationLines, frame.conversationSeparators, bounds); got != "abcdefghij" {
		t.Fatalf("selected text = %q, want %q", got, "abcdefghij")
	}
}

func TestMouseSelectionPreservesSoftWrapWhitespace(t *testing.T) {
	model := newTUIModel(8, 10, Options{})
	model.appendBlock(blockAssistant, "abc  def")
	frame := buildTerminalFrame(model)
	bounds := selectionBounds{
		start: selectionPoint{row: 1, column: 0, conversation: true},
		end:   selectionPoint{row: 2, column: 7, conversation: true},
	}

	if got := selectedConversationText(frame.conversationLines, frame.conversationSeparators, bounds); got != "abc  def" {
		t.Fatalf("selected text = %q, want %q", got, "abc  def")
	}
}

func TestMouseSelectionPreservesExplicitConversationNewlines(t *testing.T) {
	model := newTUIModel(8, 10, Options{})
	model.appendBlock(blockAssistant, "abc\ndef")
	frame := buildTerminalFrame(model)
	bounds := selectionBounds{
		start: selectionPoint{row: 1, column: 0, conversation: true},
		end:   selectionPoint{row: 2, column: 7, conversation: true},
	}

	if got := selectedConversationText(frame.conversationLines, frame.conversationSeparators, bounds); got != "abc\ndef" {
		t.Fatalf("selected text = %q, want %q", got, "abc\\ndef")
	}
}

func TestMouseClickDoesNotCopy(t *testing.T) {
	model := newTUIModel(20, 10, Options{})
	model.appendBlock(blockAssistant, "alpha")
	committed := buildTerminalFrame(model)

	var output bytes.Buffer
	if _, err := handleModelMouse(model, &output, committed, mouseEvent{kind: mousePress, column: 1, row: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := handleModelMouse(model, &output, committed, mouseEvent{kind: mouseRelease, column: 1, row: 1}); err != nil {
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

func handleModelMouse(model *tuiModel, output *bytes.Buffer, frame terminalFrame, mouse mouseEvent) (bool, error) {
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{frame: frame}, operations: operationsFor(&fakeEngine{}), controls: controlsFor(&fakeEngine{}), output: output,
		engineMessages: messages, stopped: stopped,
	}
	return controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyMouse, mouse: mouse}})
}
