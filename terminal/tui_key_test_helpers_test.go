package terminal

import (
	"context"
	"io"

	"github.com/eul-ai/eul/agent"
)

func reduceKey(model *tuiModel, key keyEvent) (tuiAction, error) {
	return handleKeyInput(model, key, buildTerminalFrame(model))
}

func handleKey(
	ctx context.Context,
	model *tuiModel,
	engine fakeEngineAPI,
	key keyEvent,
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
