package terminal

import (
	"context"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
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
	unavailable := newTUIModel(80, 24, Options{})
	if err := unavailable.insertInput("/fast"); err != nil {
		t.Fatal(err)
	}
	if unavailable.commandPickerVisible() {
		t.Fatalf("unavailable picker = %+v", unavailable.commandPicker)
	}

	missingSetter := newTUIModel(80, 24, Options{FastModeAvailable: true})
	if err := missingSetter.insertInput("/fast"); err != nil {
		t.Fatal(err)
	}
	if missingSetter.commandPickerVisible() {
		t.Fatalf("missing setter picker = %+v", missingSetter.commandPicker)
	}

	available := newTUIModel(80, 24, Options{FastModeAvailable: true, SetFastMode: func(bool) error { return nil }})
	if err := available.insertInput("/fast"); err != nil {
		t.Fatal(err)
	}
	action, err := reduceKey(available, keyEvent{code: keyEnter})
	if err != nil || action.kind != tuiActionToggleFast {
		t.Fatalf("action=%+v error=%v", action, err)
	}
	if help := commandHelpText(true); !strings.Contains(help, "/fast") {
		t.Fatalf("help = %q", help)
	}
	if help := commandHelpText(false); strings.Contains(help, "/fast") {
		t.Fatalf("help = %q", help)
	}
}

func TestCommandPickerFiltersCommandsAndSkills(t *testing.T) {
	model := newTUIModel(80, 24, Options{Skills: []agent.Skill{
		{Name: "review", Description: "Review code"},
		{Name: "Invalid Name", Description: "Cannot be invoked"},
	}})
	if err := model.insertInput("/skill:r"); err != nil {
		t.Fatal(err)
	}

	if !model.commandPickerVisible() || len(model.commandPicker.matches) != 1 {
		t.Fatalf("picker = %+v", model.commandPicker)
	}
	match := model.commandPicker.matches[0]
	if match.text != "/skill:review" || match.description != "Review code" {
		t.Fatalf("match = %+v", match)
	}
}

func TestCommandPickerEnterCompletesAndSubmits(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionHelp || len(model.input) != 0 || len(model.history) != 1 || len(model.blocks) != 0 {
		t.Fatalf("submission action=%+v input=%q history=%q blocks=%+v", action, model.input, model.history, model.blocks)
	}
	controller := tuiController{model: model}
	if _, err := controller.applyAction(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(model.blocks) != 1 || !strings.Contains(model.blocks[0].text, "/goal clear") {
		t.Fatalf("help blocks = %+v", model.blocks)
	}
}

func TestCommandPickerLetsExactCommandSubmit(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionNewSession || len(model.input) != 0 || len(model.history) != 1 {
		t.Fatalf("action=%+v input=%q history=%q", action, model.input, model.history)
	}
}

func TestCommandPickerEnterExecutesArrowSelection(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/c"); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionCompact || len(model.input) != 0 || len(model.history) != 1 || model.history[0] != "/compact" {
		t.Fatalf("action=%+v input=%q history=%q", action, model.input, model.history)
	}
}

func TestCommandPickerNavigationAndTabReplaceCommandFragment(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/goXX existing"); err != nil {
		t.Fatal(err)
	}
	model.cursor = len([]rune("/go"))
	model.refreshCommandPicker(true)

	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keyEvent{code: keyTab}); err != nil {
		t.Fatal(err)
	}
	if got, want := string(model.input), "/goal clear existing"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func TestCommandPickerEscapeDismissesUntilEdit(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/"); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keyEvent{code: keyEscape}); err != nil {
		t.Fatal(err)
	}
	if model.commandPickerVisible() {
		t.Fatal("picker remained visible after escape")
	}

	model.moveRight()
	if model.commandPickerVisible() {
		t.Fatal("picker reopened without an edit")
	}
	if _, err := reduceKey(model, keyEvent{code: keyText, text: "h"}); err != nil {
		t.Fatal(err)
	}
	if !model.commandPickerVisible() {
		t.Fatal("picker did not reopen after an edit")
	}
}

func TestHiddenCommandPickerDoesNotInterceptEscape(t *testing.T) {
	model := newTUIModel(80, 5, Options{})
	model.running = true
	if err := model.insertInput("/"); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keyEvent{code: keyEscape})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionCancel || model.interrupted {
		t.Fatalf("action=%+v interrupted=%t", action, model.interrupted)
	}
}

func TestCommandPickerOnlySuggestsRunnableCommandsDuringTurn(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	if err := model.insertInput("/"); err != nil {
		t.Fatal(err)
	}

	var matches []string
	for _, match := range model.commandPicker.matches {
		matches = append(matches, match.text)
	}
	if got, want := strings.Join(matches, ","), "/help,/goal,/goal clear"; got != want {
		t.Fatalf("matches = %q, want %q", got, want)
	}
}

func TestCommandPickerSuggestsRunnableCommandDuringTurn(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}
	if !model.commandPickerVisible() || len(model.commandPicker.matches) != 1 || model.commandPicker.matches[0].text != "/help" {
		t.Fatalf("picker = %+v", model.commandPicker)
	}
}

func TestFilePickerCanOpenInCommandArguments(t *testing.T) {
	model := newTUIModel(80, 24, Options{WorkingDirectory: t.TempDir()})
	if err := model.insertInput("/goal inspect @"); err != nil {
		t.Fatal(err)
	}

	if model.commandPickerVisible() || !model.filePickerVisible() {
		t.Fatalf("command picker=%+v file picker=%+v", model.commandPicker, model.filePicker)
	}
}

func TestRenderCommandPickerShowsSelectionAndDescription(t *testing.T) {
	model := newTUIModel(40, 12, Options{})
	if err := model.insertInput("/he"); err != nil {
		t.Fatal(err)
	}

	lines := renderCommandPicker(model, model.commandPickerHeight())
	if len(lines) != 1 || lines[0].text != "/help" || lines[0].rightText != "show this help" || lines[0].prefixText != "> " || !lines[0].style.paintBackground {
		t.Fatalf("lines = %+v", lines)
	}

	frame := buildTerminalFrame(model)
	if frame.layout.pickerHeight != 1 || !strings.Contains(frame.plainRows[frame.layout.pickerRow-1], "/help") {
		t.Fatalf("layout=%+v rows=%q", frame.layout, frame.plainRows)
	}
}
