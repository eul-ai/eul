package terminal

import (
	"context"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/terminal/keys"
)

func TestCommandReferenceRequiresCommandPositionAndTracksReplacement(t *testing.T) {
	tests := []struct {
		input  string
		cursor int
		start  int
		end    int
		query  string
		ok     bool
	}{
		{input: "/", cursor: 1, start: 0, end: 1, query: "/", ok: true},
		{input: "  /hexx argument", cursor: 5, start: 2, end: 7, query: "/he", ok: true},
		{input: "explain /", cursor: 9},
		{input: "/help", cursor: 0},
	}
	for _, test := range tests {
		input := []rune(test.input)
		start, end, query, ok := commandReference(input, test.cursor)
		if start != test.start || end != test.end || query != test.query || ok != test.ok {
			t.Fatalf("commandReference(%q, %d) = %d,%d,%q,%t, want %d,%d,%q,%t", test.input, test.cursor, start, end, query, ok, test.start, test.end, test.query, test.ok)
		}
	}
}

func TestFastCommandOnlyAppearsWhenAvailable(t *testing.T) {
	unavailable := newTestModel(80, 24, nil)
	if err := unavailable.insertInput("/fast"); err != nil {
		t.Fatal(err)
	}
	if unavailable.commandPickerVisible() {
		t.Fatalf("unavailable picker = %+v", unavailable.commandPicker)
	}

	available := newTestModel(80, 24, &fakeSession{info: session.Info{Model: backend.ModelMetadata{FastMode: true}}})
	if err := available.insertInput("/fast"); err != nil {
		t.Fatal(err)
	}
	action, err := reduceKey(available, keys.Event{Code: keys.Enter})
	if err != nil || action.kind != tuiActionCommand || action.command.name != "/fast" {
		t.Fatalf("action=%+v error=%v", action, err)
	}
}

func TestCommandPickerFiltersCommandsAndSkills(t *testing.T) {
	model := newTestModel(80, 24, &fakeSession{info: session.Info{Skills: []skill.Skill{
		{Name: "review", Description: "Review code"},
		{Name: "Invalid Name", Description: "Cannot be invoked"},
	}}})
	if err := model.insertInput("/skill:r"); err != nil {
		t.Fatal(err)
	}

	if !model.commandPickerVisible() || len(model.commandPicker.items) != 1 {
		t.Fatalf("picker = %+v", model.commandPicker)
	}
	match := model.commandPicker.items[0]
	if match.text != "/skill:review" || match.description != "Review code" {
		t.Fatalf("match = %+v", match)
	}
}

func TestCommandPickerEnterCompletesAndSubmits(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keys.Event{Code: keys.Enter})
	if err != nil {
		t.Fatal(err)
	}
	if action.command.name != "/help" || len(model.input) != 0 || len(model.history) != 1 || len(model.entries) != 0 {
		t.Fatalf("submission action=%+v input=%q history=%q blocks=%+v", action, model.inputText(), model.history, model.entries)
	}
	controller := tuiController{model: model}
	if _, err := controller.applyAction(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(model.entries) != 1 || model.entries[0].Kind != session.EntryInfo {
		t.Fatalf("help blocks = %+v", model.entries)
	}
}

func TestCommandPickerLetsExactCommandSubmit(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keys.Event{Code: keys.Enter})
	if err != nil {
		t.Fatal(err)
	}
	if action.command.name != "/new" || len(model.input) != 0 || len(model.history) != 1 {
		t.Fatalf("action=%+v input=%q history=%q", action, model.inputText(), model.history)
	}
}

func TestCommandPickerEnterExecutesArrowSelection(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("/c"); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keys.Event{Code: keys.Down}); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keys.Event{Code: keys.Enter})
	if err != nil {
		t.Fatal(err)
	}
	if action.command.name != "/compact" || len(model.input) != 0 || len(model.history) != 1 || model.history[0] != "/compact" {
		t.Fatalf("action=%+v input=%q history=%q", action, model.inputText(), model.history)
	}
}

func TestCommandPickerNavigationAndTabReplaceCommandFragment(t *testing.T) {
	model := newTestModel(80, 24, &fakeSession{info: session.Info{Skills: []skill.Skill{{Name: "review"}, {Name: "refactor"}}}})
	if err := model.insertInput("/skXX existing"); err != nil {
		t.Fatal(err)
	}
	model.cursor = len([]rune("/sk"))
	model.refreshCommandPicker(true)

	if _, err := reduceKey(model, keys.Event{Code: keys.Down}); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keys.Event{Code: keys.Tab}); err != nil {
		t.Fatal(err)
	}
	if got, want := model.inputText(), "/skill:refactor existing"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func TestCommandPickerReplacementStopsBeforeImage(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("image")}); err != nil {
		t.Fatal(err)
	}
	model.cursor = len(editorItemsFromText("/he"))
	model.refreshCommandPicker(true)

	if err := model.applyCommandPickerSelection(); err != nil {
		t.Fatal(err)
	}
	content := editorContent(model.input)
	if len(content) != 2 || content[0].Text != "/help" || content[1].Kind != agent.ContentPartImage {
		t.Fatalf("content = %+v", content)
	}
}

func TestCommandPickerEscapeDismissesUntilEdit(t *testing.T) {
	model := newTestModel(80, 24, nil)
	if err := model.insertInput("/"); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keys.Event{Code: keys.Escape}); err != nil {
		t.Fatal(err)
	}
	if model.commandPickerVisible() {
		t.Fatal("picker remained visible after escape")
	}

	model.moveRight()
	if model.commandPickerVisible() {
		t.Fatal("picker reopened without an edit")
	}
	if _, err := reduceKey(model, keys.Event{Code: keys.Text, Text: "h"}); err != nil {
		t.Fatal(err)
	}
	if !model.commandPickerVisible() {
		t.Fatal("picker did not reopen after an edit")
	}
}

func TestHiddenCommandPickerDoesNotInterceptEscape(t *testing.T) {
	model := newTestModel(80, 5, nil)
	setRunning(model)
	if err := model.insertInput("/"); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keys.Event{Code: keys.Escape})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionCancel || model.interrupted {
		t.Fatalf("action=%+v interrupted=%t", action, model.interrupted)
	}
}

func TestCommandPickerOnlySuggestsRunnableCommandsDuringTurn(t *testing.T) {
	model := newTestModel(80, 24, nil)
	setRunning(model)
	if err := model.insertInput("/"); err != nil {
		t.Fatal(err)
	}

	var matches []string
	for _, match := range model.commandPicker.items {
		matches = append(matches, match.text)
	}
	if got, want := strings.Join(matches, ","), "/help,/goal"; got != want {
		t.Fatalf("matches = %q, want %q", got, want)
	}
}

func TestCommandPickerSuggestsRunnableCommandDuringTurn(t *testing.T) {
	model := newTestModel(80, 24, nil)
	setRunning(model)
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}
	if !model.commandPickerVisible() || len(model.commandPicker.items) != 1 || model.commandPicker.items[0].text != "/help" {
		t.Fatalf("picker = %+v", model.commandPicker)
	}
}

func TestFilePickerCanOpenInCommandArguments(t *testing.T) {
	model := newTestModel(80, 24, &fakeSession{settings: session.Settings{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("/goal inspect @"); err != nil {
		t.Fatal(err)
	}

	if model.commandPickerVisible() || !model.filePickerVisible() {
		t.Fatalf("command picker=%+v file picker=%+v", model.commandPicker, model.filePicker)
	}
}

func TestRenderCommandPickerShowsSelectionAndDescription(t *testing.T) {
	model := newTestModel(40, 12, nil)
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}

	lines := renderCommandPicker(model, model.pickerHeight())
	if len(lines) != 1 || lines[0].text != "/help" || lines[0].prefixText != "> " || !lines[0].style.paintBackground {
		t.Fatalf("lines = %+v", lines)
	}

	frame := buildTerminalFrame(model)
	if frame.layout.pickerHeight != 1 || !strings.Contains(frame.plainRows[frame.layout.pickerRow-1], "/help") {
		t.Fatalf("layout=%+v rows=%q", frame.layout, frame.plainRows)
	}
}
