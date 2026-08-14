package agent

import (
	"context"
	"errors"
)

type engineTurn struct {
	engine               *Engine
	ctx                  context.Context
	sink                 EventSink
	current              conversationState
	result               RunResult
	inboxBatch           InboxBatch
	responseContinuation conversationState
}

func (turn *engineTurn) run() (RunResult, error) {
	for {
		if err := turn.ctx.Err(); err != nil {
			turn.current.checkpoint(turn.engine)
			return RunResult{}, err
		}

		prepared := turn.prepareGeneration()
		if prepared.err != nil {
			turn.current.checkpoint(turn.engine)
			return RunResult{}, prepared.err
		}

		generated := turn.generate(prepared)
		if generated.err != nil {
			turn.current.checkpoint(turn.engine)
			if ctxErr := turn.ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			return RunResult{}, generated.err
		}

		done, err := turn.reconcileResponse(generated.response, generated.toolEvents)
		if err != nil {
			return RunResult{}, err
		}
		if done {
			return turn.result, nil
		}
	}
}

func (turn *engineTurn) prepareGeneration() generationPreparation {
	prepared := turn.engine.prepareGeneration(turn.ctx, turn.sink, turn.current)
	turn.current = prepared.current
	turn.inboxBatch = prepared.inboxBatch
	addUsage(&turn.result.Usage, prepared.compactedUsage)
	return prepared
}

func (turn *engineTurn) generate(prepared generationPreparation) generationOutcome {
	generated := turn.engine.generateWithRecovery(
		turn.ctx,
		turn.sink,
		prepared.request,
		prepared.ordinaryRequest,
		turn.inboxBatch,
		turn.current,
	)
	turn.current = generated.current
	addUsage(&turn.result.Usage, generated.compactedUsage)
	return generated
}

func (turn *engineTurn) reconcileResponse(response Response, toolEvents *toolEventTracker) (bool, error) {
	turn.responseContinuation = conversationState{state: response.State, usage: response.Usage}
	addUsage(&turn.result.Usage, response.Usage)
	if err := emit(turn.sink, Event{Kind: EventContextUsage, Usage: response.Usage}); err != nil {
		turn.responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
		turn.responseContinuation.checkpoint(turn.engine)
		return false, err
	}
	if err := toolEvents.reconcileFinal(response.ToolCalls); err != nil {
		turn.responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
		turn.responseContinuation.checkpoint(turn.engine)
		return false, err
	}

	if err := turn.settleInboxBatch(response.ToolCalls); err != nil {
		return false, err
	}
	if len(response.ToolCalls) == 0 {
		return turn.continueWithoutTools(response)
	}
	return false, turn.executeTools(response.ToolCalls, toolEvents)
}

func (turn *engineTurn) settleInboxBatch(toolCalls []ToolCall) error {
	if len(turn.inboxBatch.MessageIDs) == 0 {
		return nil
	}

	checkpoint := turn.responseContinuation
	if len(toolCalls) > 0 {
		checkpoint.inputs = unexecutedToolInputs(toolCalls, errors.New("tool execution has not completed"))
	}
	checkpoint.checkpoint(turn.engine)
	if err := turn.engine.commitCheckpoint(checkpoint, turn.sink); err != nil {
		return err
	}
	return turn.engine.acknowledgeInbox(turn.inboxBatch)
}

func (turn *engineTurn) continueWithoutTools(response Response) (bool, error) {
	if len(turn.inboxBatch.MessageIDs) == 0 {
		if err := turn.engine.commitCheckpoint(turn.responseContinuation, turn.sink); err != nil {
			return false, err
		}
	}

	next, ok := turn.engine.continuations.next(continuationBeforeSettle)
	if !ok {
		if !turn.engine.settleInbox() {
			turn.current = turn.responseContinuation
			return false, nil
		}
		turn.result.Text = response.Text
		return true, nil
	}

	if err := deliverContinuation(&turn.responseContinuation, next, turn.sink); err != nil {
		turn.responseContinuation.checkpoint(turn.engine)
		return false, err
	}
	turn.current = turn.responseContinuation
	return false, nil
}

func (turn *engineTurn) executeTools(calls []ToolCall, toolEvents *toolEventTracker) error {
	inputs, err := turn.engine.executeToolRound(turn.ctx, calls, toolEvents)
	turn.responseContinuation.inputs = inputs
	turn.current = turn.responseContinuation
	if err != nil {
		turn.current.checkpoint(turn.engine)
		return err
	}
	if err := turn.engine.commitCheckpoint(turn.current, turn.sink); err != nil {
		return err
	}

	if next, ok := turn.engine.continuations.next(continuationAfterToolBatch); ok {
		if err := deliverContinuation(&turn.current, next, turn.sink); err != nil {
			turn.current.checkpoint(turn.engine)
			return err
		}
	}
	return nil
}
