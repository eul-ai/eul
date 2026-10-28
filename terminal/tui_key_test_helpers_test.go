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

func handleKey(
	ctx context.Context,
	model *tuiModel,
	engine fakeEngineAPI,
	key keys.Event,
	messages chan<- engineMessage,
	stopped <-chan struct{},
	turnCancel *context.CancelFunc,
) (bool, error) {
	renderer := &tuiRenderer{}
	_ = renderModel(renderer, model)
	var setThinkingLevel func(agent.ThinkingLevel) error
	if model.thinkingSelectionAvailable {
		setThinkingLevel = func(agent.ThinkingLevel) error { return nil }
	}
	controller := tuiController{
		model:      model,
		renderer:   renderer,
		operations: operationsFor(engine),
		controls: Controls{
			Steer:            engine.Steer,
			ClearSteering:    engine.ClearSteering,
			SetGoal:          engine.SetGoal,
			Goal:             engine.Goal,
			ClearGoal:        engine.ClearGoal,
			SetThinkingLevel: setThinkingLevel,
		},
		output:         io.Discard,
		engineMessages: messages,
		stopped:        stopped,
		turnCancel:     *turnCancel,
	}
	exit, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: key})
	*turnCancel = controller.turnCancel
	return exit, err
}

func handleModelKey(model *tuiModel, key keys.Event, setThinkingLevel func(agent.ThinkingLevel) error) (bool, error) {
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, operations: operationsFor(&fakeEngine{}), controls: Controls{SetThinkingLevel: setThinkingLevel}, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	return controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: key})
}
