package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type conversationState struct {
	state  []byte
	usage  Usage
	inputs []Input
}

type turn struct {
	engine  *Engine
	ctx     context.Context
	sink    EventSink
	current conversationState
	inbox   InboxBatch
	tools   *toolEventTracker
}

func (t *turn) run() (RunResult, error) {
	for {
		if err := t.ctx.Err(); err != nil {
			return RunResult{}, err
		}

		response, err := t.generate()
		if err != nil {
			if ctxErr := t.ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			return RunResult{}, err
		}

		done, err := t.accept(response)
		switch {
		case err != nil:
			return RunResult{}, err
		case done:
			return RunResult{Text: response.Text}, nil
		}
	}
}

func (t *turn) request() Request {
	thinkingLevel, fastMode := t.engine.settings.Snapshot()
	return Request{
		SessionID:     t.engine.sessionID,
		Model:         t.engine.model,
		ThinkingLevel: thinkingLevel,
		FastMode:      fastMode,
		Instructions:  t.engine.instructions,
		Inputs:        slices.Clip(t.current.inputs),
		Tools:         t.engine.definitions,
		State:         t.current.state,
	}
}

func (t *turn) decorate(request Request) Request {
	if strings.TrimSpace(t.inbox.Text) != "" {
		request.Inputs = append(request.Inputs, NewInboxInput(t.inbox.Text))
	}
	if instructions := strings.TrimSpace(t.inbox.Instructions); instructions != "" {
		request.Instructions = strings.TrimSpace(request.Instructions) + "\n\n" + instructions
	}
	return request
}

func (t *turn) compact() error {
	compactor, ok := t.engine.provider.(Compactor)
	if !ok {
		return errors.New("agent: context compaction is unavailable")
	}
	if err := t.sink(Event{Kind: EventCompactionStart}); err != nil {
		return err
	}

	compacted, err := compactor.Compact(t.ctx, t.request())
	if err != nil {
		if ctxErr := t.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("agent: compact context: %w", err)
	}
	if len(compacted.State) == 0 {
		return errors.New("agent: compact context: provider returned empty state")
	}

	t.current = conversationState{state: compacted.State}
	if err := t.sink(Event{Kind: EventCompactionEnd, Usage: compacted.Usage}); err != nil {
		return err
	}
	return t.commit()
}

func (t *turn) generate() (Response, error) {
	if t.engine.inbox != nil {
		t.inbox = t.engine.inbox.Snapshot()
	}
	request := t.decorate(t.request())
	if compactor, ok := t.engine.provider.(Compactor); ok && compactor.ShouldCompact(request, t.current.usage) {
		if err := t.compact(); err != nil {
			return Response{}, err
		}
		request = t.decorate(t.request())
	}

	response, observed, err := t.generateWithRetry(request)
	if err == nil || observed || t.ctx.Err() != nil || !t.compactsAfter(err) {
		return response, err
	}
	if err := t.compact(); err != nil {
		return Response{}, err
	}
	response, _, err = t.generateWithRetry(t.decorate(t.request()))
	return response, err
}

func (t *turn) compactsAfter(err error) bool {
	_, canCompact := t.engine.provider.(Compactor)
	policy, hasPolicy := t.engine.provider.(CompactionErrorPolicy)
	return canCompact && hasPolicy && policy.ShouldCompactAfterError(t.request(), err)
}

func (t *turn) generateWithRetry(request Request) (Response, bool, error) {
	retryPolicy, canRetry := t.engine.provider.(GenerationRetryPolicy)
	for failedAttempts := 1; ; failedAttempts++ {
		tools := newToolEventTracker(t.engine.tools, t.sink)
		t.tools = tools
		response, err := t.engine.provider.Generate(t.ctx, request, StreamObserver{
			Text: func(text string) error {
				return tools.emitProvider(Event{Kind: EventAssistantText, Text: text})
			},
			Reasoning: func(text string) error {
				return tools.emitProvider(Event{Kind: EventAssistantReasoning, Text: text})
			},
			ToolCall: tools.observeSnapshot,
		})
		if err == nil {
			return response, false, nil
		}

		observed := tools.sawObserverEvent()
		closeErr := tools.closeRemaining(err)
		if ctxErr := t.ctx.Err(); ctxErr != nil {
			return Response{}, observed, ctxErr
		}
		if closeErr != nil {
			return Response{}, true, closeErr
		}

		delay, retry := time.Duration(0), false
		if canRetry {
			delay, retry = retryPolicy.RetryGeneration(err, failedAttempts)
		}
		if !retry {
			return Response{}, observed, fmt.Errorf("agent: generate response: %w", err)
		}
		if err := t.sink(Event{Kind: EventGenerationRetry, Attempt: failedAttempts + 1}); err != nil {
			return Response{}, true, err
		}
		if err := waitForRetry(t.ctx, delay); err != nil {
			return Response{}, observed, err
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *turn) accept(response Response) (bool, error) {
	t.current = conversationState{state: response.State, usage: response.Usage}
	if err := t.sink(Event{Kind: EventContextUsage, Usage: response.Usage}); err != nil {
		t.current.inputs = unexecutedToolInputs(response.ToolCalls, err)
		return false, err
	}
	if err := t.tools.reconcileFinal(response.ToolCalls); err != nil {
		t.current.inputs = unexecutedToolInputs(response.ToolCalls, err)
		return false, err
	}
	if err := t.acknowledgeInbox(response.ToolCalls); err != nil {
		return false, err
	}
	if len(response.ToolCalls) > 0 {
		return false, t.executeTools(response.ToolCalls)
	}

	if len(t.inbox.MessageIDs) == 0 {
		if err := t.commit(); err != nil {
			return false, err
		}
	}
	if next, ok := t.engine.continuations.nextBeforeSettle(); ok {
		return false, t.deliver(next)
	}
	return t.engine.inbox == nil || len(t.engine.inbox.Snapshot().MessageIDs) == 0, nil
}

func (t *turn) acknowledgeInbox(calls []ToolCall) error {
	if len(t.inbox.MessageIDs) == 0 {
		return nil
	}

	if len(calls) > 0 {
		t.current.inputs = unexecutedToolInputs(calls, errors.New("tool execution has not completed"))
	}
	if err := t.commit(); err != nil {
		return err
	}
	return t.engine.inbox.Acknowledge(t.inbox)
}

func (t *turn) deliver(next pendingContinuation) error {
	event := Event{Kind: EventSteering, Content: next.content}
	if next.kind == continuationGoal {
		event = Event{Kind: EventGoalContinuation}
	}
	if err := t.sink(event); err != nil {
		return err
	}

	t.current.inputs = append(t.current.inputs, NewUserInput(next.content...))
	return nil
}

func (t *turn) commit() error {
	t.engine.conversation = t.current
	if !t.engine.checkpointing {
		return nil
	}

	checkpoint := t.engine.checkpointLocked()
	return t.sink(Event{Kind: EventCheckpoint, Checkpoint: &checkpoint})
}
