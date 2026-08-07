package terminal

import (
	"testing"

	"yaah/agent"
)

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
				if string(model.input) != "draft" {
					t.Fatalf("input = %q", model.input)
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
				if !model.interrupted || model.activity.kind != activityCanceling {
					t.Fatalf("interrupted=%v activity=%+v", model.interrupted, model.activity)
				}
			},
		},
		{
			name: "reset",
			setup: func(t *testing.T, model *tuiModel) {
				model.appendBlock(blockAssistant, "keep until reset effect")
				if err := model.insertInput("/clear"); err != nil {
					t.Fatal(err)
				}
			},
			key:      keyEvent{code: keyEnter},
			wantKind: tuiActionReset,
			check: func(t *testing.T, model *tuiModel) {
				if len(model.history) != 1 || model.history[0] != "/clear" || len(model.blocks) != 1 {
					t.Fatalf("history=%q blocks=%+v", model.history, model.blocks)
				}
			},
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
				if !model.running || len(model.history) != 1 || len(model.blocks) != 1 || model.blocks[0].kind != blockUser {
					t.Fatalf("running=%v history=%q blocks=%+v", model.running, model.history, model.blocks)
				}
			},
		},
		{
			name: "set thinking",
			options: Options{
				ThinkingLevel: agent.ThinkingMedium,
				SetThinkingLevel: func(agent.ThinkingLevel) error {
					panic("reducer invoked external thinking setter")
				},
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
			name: "running ignores editing",
			setup: func(t *testing.T, model *tuiModel) {
				model.running = true
			},
			key: keyEvent{code: keyText, text: "ignored"},
			check: func(t *testing.T, model *tuiModel) {
				if len(model.input) != 0 {
					t.Fatalf("input = %q", model.input)
				}
			},
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
