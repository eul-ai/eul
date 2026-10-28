package terminal

import (
	"context"
	"io"
	"slices"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/terminal/keys"
)

func reduceKey(model *tuiModel, key keys.Event) (tuiAction, error) {
	return handleKeyInput(model, key, buildTerminalFrame(model))
}

func commandAction(name, arg string) tuiAction {
	index := slices.IndexFunc(slashCommands, func(command slashCommand) bool { return command.name == name })
	return tuiAction{kind: tuiActionCommand, command: slashCommands[index], text: arg}
}

func appendUser(model *tuiModel, content []agent.ContentPart) {
	session := testSession(model)
	session.mu.Lock()
	session.transcript.AppendUser(content)
	session.mu.Unlock()
	model.syncConversation()
}

func newTestController(model *tuiModel) *tuiController {
	return &tuiController{
		model:             model,
		renderer:          &tuiRenderer{},
		controls:          controlsFor(&fakeEngine{}),
		output:            io.Discard,
		stopped:           make(chan struct{}),
		clipboardRequests: map[uint64]context.CancelFunc{},
	}
}

func handleKey(ctx context.Context, model *tuiModel, key keys.Event) (bool, error) {
	controller := newTestController(model)
	_ = renderModel(controller.renderer, model)
	if model.thinkingSelectionAvailable {
		controller.controls.SetThinkingLevel = func(agent.ThinkingLevel) error { return nil }
	}
	return controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: key})
}

func handleModelKey(model *tuiModel, key keys.Event, setThinkingLevel func(agent.ThinkingLevel) error) (bool, error) {
	controller := newTestController(model)
	controller.controls = Controls{SetThinkingLevel: setThinkingLevel}
	return controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: key})
}
