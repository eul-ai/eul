package terminal

import (
	"context"
	"io"

	"github.com/eul-ai/eul/agent"
)

func reduceKey(model *tuiModel, key keyEvent) (tuiAction, error) {
	return reduceKeyWithFrame(model, key, buildTerminalFrame(model))
}

func handleKey(
	ctx context.Context,
	model *tuiModel,
	engine Engine,
	key keyEvent,
	messages chan<- engineMessage,
	stopped <-chan struct{},
	turnCancel *context.CancelFunc,
) (bool, error) {
	renderer := &tuiRenderer{}
	_ = renderer.render(model)
	var setThinkingLevel func(agent.ThinkingLevel) error
	if model.thinkingSelectionAvailable {
		setThinkingLevel = func(agent.ThinkingLevel) error { return nil }
	}
	controller := tuiController{
		model:            model,
		renderer:         renderer,
		engine:           engine,
		output:           io.Discard,
		engineMessages:   messages,
		stopped:          stopped,
		setThinkingLevel: setThinkingLevel,
		turnCancel:       *turnCancel,
	}
	exit, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: key})
	*turnCancel = controller.turnCancel
	return exit, err
}
