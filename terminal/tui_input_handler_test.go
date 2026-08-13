package terminal

import (
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestPermissionKeysTakePriority(t *testing.T) {
	for _, test := range []struct {
		name string
		key  keyEvent
		want tuiActionKind
	}{
		{name: "allow", key: keyEvent{code: keyText, text: "y"}, want: tuiActionAllowPermission},
		{name: "deny", key: keyEvent{code: keyText, text: "n"}, want: tuiActionDenyPermission},
		{name: "enter denies", key: keyEvent{code: keyEnter}, want: tuiActionDenyPermission},
		{name: "escape denies", key: keyEvent{code: keyEscape}, want: tuiActionDenyPermission},
		{name: "cancel", key: keyEvent{code: keyCtrlC}, want: tuiActionCancel},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newTUIModel(80, 24, Options{})
			model.running = true
			model.showPermission(PermissionRequest{Title: "Network access", Detail: "git push"}, 1, 1)
			if err := model.insertInput("queued steering"); err != nil {
				t.Fatal(err)
			}

			action, err := reduceKey(model, test.key)
			if err != nil || action.kind != test.want {
				t.Fatalf("action = %+v, error = %v", action, err)
			}
			if model.inputText() != "queued steering" {
				t.Fatalf("permission key changed input to %q", model.inputText())
			}
		})
	}
}

func TestPermissionSelectionAndScrolling(t *testing.T) {
	model := newTUIModel(40, 12, Options{})
	model.running = true
	model.showPermission(PermissionRequest{
		Title:  "Permission requested",
		Detail: strings.Repeat("long command ", 30),
	}, 1, 1)

	if action, err := reduceKey(model, keyEvent{code: keyRight}); err != nil || action.kind != tuiActionNone || !model.permission.allowSelected {
		t.Fatalf("right action=%+v err=%v permission=%+v", action, err, model.permission)
	}
	if action, err := reduceKey(model, keyEvent{code: keyEnter}); err != nil || action.kind != tuiActionAllowPermission {
		t.Fatalf("enter action=%+v err=%v", action, err)
	}
	if action, err := reduceKey(model, keyEvent{code: keyLeft}); err != nil || action.kind != tuiActionNone || model.permission.allowSelected {
		t.Fatalf("left action=%+v err=%v permission=%+v", action, err, model.permission)
	}
	if action, err := reduceKey(model, keyEvent{code: keyDown}); err != nil || action.kind != tuiActionNone || model.permission.scroll == 0 {
		t.Fatalf("down action=%+v err=%v scroll=%d", action, err, model.permission.scroll)
	}
}

func TestFilePickerKeysTakePriorityOverEditorActions(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if _, err := reduceKey(model, keyEvent{code: keyText, text: "@"}); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"a.go", "b.go"}})
	if !model.filePickerVisible() {
		t.Fatal("picker did not open")
	}

	if action, err := reduceKey(model, keyEvent{code: keyDown}); err != nil || action.kind != tuiActionNone {
		t.Fatalf("down action = %+v, err = %v", action, err)
	}
	if action, err := reduceKey(model, keyEvent{code: keyEnter}); err != nil || action.kind != tuiActionNone {
		t.Fatalf("enter action = %+v, err = %v", action, err)
	}
	if got := model.inputText(); got != "@b.go " {
		t.Fatalf("input = %q", got)
	}
	if model.running || len(model.history) != 0 {
		t.Fatalf("selection submitted prompt: running=%t history=%q", model.running, model.history)
	}

	model.clearInput()
	if _, err := reduceKey(model, keyEvent{code: keyText, text: "@"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keyEvent{code: keyEscape}); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "@" || model.filePickerVisible() {
		t.Fatalf("escape left input=%q picker=%+v", got, model.filePicker)
	}
}

func TestFilePickerRemainsUsableWhileRunning(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	model.running = true
	if _, err := reduceKey(model, keyEvent{code: keyText, text: "@"}); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"a.go", "b.go"}})

	if _, err := reduceKey(model, keyEvent{code: keyDown}); err != nil {
		t.Fatal(err)
	}
	if _, err := reduceKey(model, keyEvent{code: keyEnter}); err != nil {
		t.Fatal(err)
	}
	if got := model.inputText(); got != "@b.go " || !model.running {
		t.Fatalf("input=%q running=%v", got, model.running)
	}
}

func TestFilePickerKeepsResultsButCannotApplyThemDuringSearch(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if _, err := reduceKey(model, keyEvent{code: keyText, text: "@"}); err != nil {
		t.Fatal(err)
	}
	request := takePickerRequest(t, model)
	model.applyFileSearchResult(fileSearchResult{id: request.id, paths: []string{"a.go", "b.go", "c.go"}})
	originalHeight := model.filePickerHeight()

	if _, err := reduceKey(model, keyEvent{code: keyText, text: "a"}); err != nil {
		t.Fatal(err)
	}
	if !model.filePicker.loading || model.filePickerHeight() != originalHeight || len(model.filePicker.matches) != 3 {
		t.Fatalf("picker changed while searching: %+v", model.filePicker)
	}
	if action, err := reduceKey(model, keyEvent{code: keyEnter}); err != nil || action.kind != tuiActionNone {
		t.Fatalf("enter action = %+v, err = %v", action, err)
	}
	if got := model.inputText(); got != "@a" {
		t.Fatalf("stale selection changed input to %q", got)
	}
}

func TestHiddenFilePickerDoesNotInterceptEditorKeys(t *testing.T) {
	model := newTUIModel(80, 5, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	if model.filePickerVisible() {
		t.Fatal("picker is visible without layout space")
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionSubmit || action.prompt != "@" {
		t.Fatalf("enter action = %+v", action)
	}
}

func TestReduceKeyActions(t *testing.T) {
	tests := []struct {
		name       string
		options    Options
		setup      func(*testing.T, *tuiModel)
		key        keyEvent
		wantKind   tuiActionKind
		wantPrompt string
		wantLevel  agent.ThinkingLevel
		check      func(*testing.T, *tuiModel)
	}{
		{
			name: "none",
			key:  keyEvent{code: keyText, text: "draft"},
			check: func(t *testing.T, model *tuiModel) {
				if model.inputText() != "draft" {
					t.Fatalf("input = %q", model.inputText())
				}
			},
		},
		{
			name: "cancel",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				model.activity = activity{kind: activityThinking}
			},
			key:      keyEvent{code: keyCtrlC},
			wantKind: tuiActionCancel,
			check: func(t *testing.T, model *tuiModel) {
				if model.interrupted || model.activity.kind != activityThinking {
					t.Fatalf("interrupted=%v activity=%+v", model.interrupted, model.activity)
				}
			},
		},
		{
			name: "cancel with escape",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				model.activity = activity{kind: activityThinking}
			},
			key:      keyEvent{code: keyEscape},
			wantKind: tuiActionCancel,
			check: func(t *testing.T, model *tuiModel) {
				if model.interrupted || model.activity.kind != activityThinking {
					t.Fatalf("interrupted=%v activity=%+v", model.interrupted, model.activity)
				}
			},
		},
		{
			name: "new session",
			setup: func(t *testing.T, model *tuiModel) {
				model.appendBlock(blockAssistant, "keep in current session")
				if err := model.insertInput("/new"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionNewSession,
			check: func(t *testing.T, model *tuiModel) {
				if len(model.history) != 1 || model.history[0] != "/new" || len(model.blocks) != 1 {
					t.Fatalf("history=%q blocks=%+v", model.history, model.blocks)
				}
			},
		},
		{
			name: "compact",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/compact"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionCompact,
		},
		{
			name: "exit",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/exit"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionExit,
			check: func(t *testing.T, model *tuiModel) {
				if len(model.history) != 1 || model.history[0] != "/exit" {
					t.Fatalf("history = %q", model.history)
				}
			},
		},
		{
			name: "show goal",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/goal"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionShowGoal,
		},
		{
			name: "set goal",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/goal  finish migration  "); err != nil {
					t.Fatal(err)
				}
			},
			key:        keyEvent{code: keyEnter},
			wantKind:   tuiActionSetGoal,
			wantPrompt: "finish migration",
		},
		{
			name: "clear goal",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/goal clear"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionClearGoal,
		},
		{
			name: "submit",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput(" hello "); err != nil {
					t.Fatal(err)
				}
			},
			key:        keyEvent{code: keyEnter},
			wantKind:   tuiActionSubmit,
			wantPrompt: " hello ",
			check: func(t *testing.T, model *tuiModel) {
				if model.running || len(model.history) != 0 || len(model.blocks) != 0 || model.inputText() != " hello " {
					t.Fatalf("running=%v history=%q blocks=%+v input=%q", model.running, model.history, model.blocks, model.inputText())
				}
			},
		},
		{
			name: "set thinking",
			options: Options{
				Config: Config{ThinkingLevel: agent.ThinkingMedium},
				Controls: Controls{SetThinkingLevel: func(agent.ThinkingLevel) error {
					panic("reducer invoked external thinking setter")
				}},
			},
			key:       keyEvent{code: keyShiftTab},
			wantKind:  tuiActionSetThinking,
			wantLevel: agent.ThinkingHigh,
			check: func(t *testing.T, model *tuiModel) {
				if model.thinkingLevel != agent.ThinkingMedium {
					t.Fatalf("thinking level = %q", model.thinkingLevel)
				}
			},
		},
		{
			name: "skill command",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/skill:review check tests"); err != nil {
					t.Fatal(err)
				}
			},
			key:        keyEvent{code: keyEnter},
			wantKind:   tuiActionSubmit,
			wantPrompt: "/skill:review check tests",
			check: func(t *testing.T, model *tuiModel) {
				if model.running || len(model.blocks) != 0 {
					t.Fatalf("running=%v blocks=%+v", model.running, model.blocks)
				}
			},
		},
		{
			name: "unknown command",
			setup: func(t *testing.T, model *tuiModel) {
				if err := model.insertInput("/unknown"); err != nil {
					t.Fatal(err)
				}
			},
			key: keyEvent{code: keyEnter},
			check: func(t *testing.T, model *tuiModel) {
				if len(model.history) != 1 || len(model.blocks) != 1 || model.blocks[0].kind != blockError || model.activity.detail != "unknown command" {
					t.Fatalf("history=%q blocks=%+v activity=%+v", model.history, model.blocks, model.activity)
				}
			},
		},
		{
			name: "running edits draft",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
			},
			key: keyEvent{code: keyText, text: "draft"},
			check: func(t *testing.T, model *tuiModel) {
				if model.inputText() != "draft" {
					t.Fatalf("input = %q", model.inputText())
				}
			},
		},
		{
			name: "running queues steering",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				if err := model.insertInput("steer"); err != nil {
					t.Fatal(err)
				}
			},
			key:        keyEvent{code: keyEnter},
			wantKind:   tuiActionSteer,
			wantPrompt: "steer",
			check: func(t *testing.T, model *tuiModel) {
				if len(model.input) != 0 || len(model.history) != 1 || len(model.blocks) != 0 {
					t.Fatalf("input=%q history=%q blocks=%+v", model.inputText(), model.history, model.blocks)
				}
			},
		},
		{
			name: "running shows help",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				if err := model.insertInput("/help"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionHelp,
			check: func(t *testing.T, model *tuiModel) {
				if len(model.input) != 0 || len(model.history) != 1 {
					t.Fatalf("input=%q history=%q", model.inputText(), model.history)
				}
			},
		},
		{
			name: "running shows goal",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				if err := model.insertInput("/goal"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionShowGoal,
			check: func(t *testing.T, model *tuiModel) {
				if len(model.input) != 0 || len(model.history) != 1 {
					t.Fatalf("input=%q history=%q", model.inputText(), model.history)
				}
			},
		},
		{
			name: "running sets goal",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				if err := model.insertInput("/goal finish migration"); err != nil {
					t.Fatal(err)
				}
			},
			key:        keyEvent{code: keyEnter},
			wantKind:   tuiActionSetGoal,
			wantPrompt: "finish migration",
			check: func(t *testing.T, model *tuiModel) {
				if len(model.input) != 0 || len(model.history) != 1 {
					t.Fatalf("input=%q history=%q", model.inputText(), model.history)
				}
			},
		},
		{
			name: "running clears goal",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				if err := model.insertInput("/goal clear"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionClearGoal,
			check: func(t *testing.T, model *tuiModel) {
				if len(model.input) != 0 || len(model.history) != 1 {
					t.Fatalf("input=%q history=%q", model.inputText(), model.history)
				}
			},
		},
		{
			name: "running retains command",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				if err := model.insertInput("/new"); err != nil {
					t.Fatal(err)
				}
			},
			key: keyEvent{code: keyEnter},
			check: func(t *testing.T, model *tuiModel) {
				if model.inputText() != "/new" || model.activity.kind != activityError {
					t.Fatalf("input=%q activity=%+v", model.inputText(), model.activity)
				}
			},
		},
		{
			name: "running changes thinking",
			options: Options{
				Config: Config{ThinkingLevel: agent.ThinkingMedium},
				Controls: Controls{SetThinkingLevel: func(agent.ThinkingLevel) error {
					panic("reducer invoked external thinking setter")
				}},
			},
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
				model.activity = activity{kind: activityThinking}
			},
			key:       keyEvent{code: keyShiftTab},
			wantKind:  tuiActionSetThinking,
			wantLevel: agent.ThinkingHigh,
			check: func(t *testing.T, model *tuiModel) {
				if model.thinkingLevel != agent.ThinkingMedium || model.activity.kind != activityThinking {
					t.Fatalf("thinking=%q activity=%+v", model.thinkingLevel, model.activity)
				}
			},
		},
		{
			name:     "dequeue",
			key:      keyEvent{code: keyAltUp},
			wantKind: tuiActionDequeue,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newTUIModel(80, 24, test.options)
			if test.setup != nil {
				test.setup(t, model)
			}

			action, err := reduceKey(model, test.key)
			if err != nil {
				t.Fatal(err)
			}
			if action.kind != test.wantKind || action.prompt != test.wantPrompt || action.thinkingLevel != test.wantLevel {
				t.Fatalf("action = %+v, want kind=%d prompt=%q level=%q", action, test.wantKind, test.wantPrompt, test.wantLevel)
			}
			if test.check != nil {
				test.check(t, model)
			}
		})
	}
}

func TestImageTerminatesFilePickerToken(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{WorkingDirectory: t.TempDir()}})
	if err := model.insertInput("@"); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	if model.filePickerVisible() {
		t.Fatal("file picker remained visible across image token")
	}
}

func TestImageTerminatesSlashCommand(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/help"); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionSubmit || len(action.content) != 2 || action.content[0].Text != "/help" || action.content[1].Kind != agent.ContentPartImage {
		t.Fatalf("action = %+v", action)
	}
}

func TestImageAttachmentCanBeSubmittedWithoutText(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}

	action, err := reduceKey(model, keyEvent{code: keyEnter})
	if err != nil {
		t.Fatal(err)
	}
	if action.kind != tuiActionSubmit || len(action.content) != 1 || action.content[0].Kind != agent.ContentPartImage || action.content[0].Image == nil || action.content[0].Image.MediaType != "image/png" {
		t.Fatalf("action = %+v", action)
	}
}
