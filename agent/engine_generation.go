package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type generationPreparation struct {
	request         Request
	ordinaryRequest Request
	current         conversationState
	inboxBatch      InboxBatch
	compactedUsage  Usage
	err             error
}

func (e *Engine) prepareGeneration(ctx context.Context, sink EventSink, current conversationState) generationPreparation {
	ordinaryRequest := e.request(current)
	inboxBatch := e.snapshotInbox()
	sizingRequest := attachInbox(ordinaryRequest, inboxBatch)
	sizingRequest = e.withAdditionalInstructions(sizingRequest)
	ordinaryRequest, current, compactedUsage, err := e.compactSized(ctx, sink, ordinaryRequest, sizingRequest, current)
	request := attachInbox(ordinaryRequest, inboxBatch)
	request = e.withAdditionalInstructions(request)
	return generationPreparation{
		request:         request,
		ordinaryRequest: ordinaryRequest,
		current:         current,
		inboxBatch:      inboxBatch,
		compactedUsage:  compactedUsage,
		err:             err,
	}
}

type generationOutcome struct {
	response       Response
	toolEvents     *toolEventTracker
	current        conversationState
	compactedUsage Usage
	err            error
}

func (e *Engine) generateWithRecovery(
	ctx context.Context,
	sink EventSink,
	request Request,
	ordinaryRequest Request,
	inboxBatch InboxBatch,
	current conversationState,
) generationOutcome {
	response, toolEvents, observed, err := e.generateResponse(ctx, request, sink)
	outcome := generationOutcome{response: response, toolEvents: toolEvents, current: current, err: err}

	var generationErr *providerGenerationError
	if err == nil || observed || ctx.Err() != nil || !errors.As(err, &generationErr) {
		return outcome
	}

	request, current, compactedUsage, compacted, err := e.compactAfterError(ctx, sink, ordinaryRequest, current, err)
	outcome.current = current
	outcome.compactedUsage = compactedUsage
	outcome.err = err
	if err == nil && compacted {
		request = e.withAdditionalInstructions(request)
		request = attachInbox(request, inboxBatch)
		outcome.response, outcome.toolEvents, _, outcome.err = e.generateResponse(ctx, request, sink)
	}
	return outcome
}

func (e *Engine) expandSkillContent(content []ContentPart) ([]ContentPart, error) {
	content = cloneContentParts(content)
	firstText := -1
	for index, part := range content {
		switch {
		case part.Kind == ContentPartImage:
			return content, nil
		case part.Kind == ContentPartText && strings.TrimSpace(part.Text) != "":
			firstText = index
		}
		if firstText >= 0 {
			break
		}
	}
	if firstText < 0 || !strings.HasPrefix(strings.TrimSpace(content[firstText].Text), "/skill:") {
		return content, nil
	}

	expanded, err := expandSkillCommand(content[firstText].Text, e.skills)
	if err != nil {
		return nil, err
	}
	content[firstText].Text = expanded
	return content, nil
}

func (e *Engine) request(current conversationState) Request {
	thinkingLevel, fastMode := e.currentSettings()
	return Request{
		Model:         e.model,
		ThinkingLevel: thinkingLevel,
		FastMode:      fastMode,
		Instructions:  e.instructions,
		Inputs:        cloneInputs(current.inputs),
		Tools:         e.tools.Definitions(),
		State:         current.state,
	}
}

type providerGenerationError struct {
	err error
}

func (e *providerGenerationError) Error() string { return e.err.Error() }
func (e *providerGenerationError) Unwrap() error { return e.err }

func (e *Engine) snapshotInbox() InboxBatch {
	if e.inbox == nil {
		return InboxBatch{}
	}
	return e.inbox.SnapshotInbox()
}

func (e *Engine) withAdditionalInstructions(request Request) Request {
	if e.additionalInstructions == nil {
		return request
	}
	additional := strings.TrimSpace(e.additionalInstructions())
	if additional == "" {
		return request
	}
	request.Instructions = strings.TrimSpace(request.Instructions) + "\n\n" + additional
	return request
}

func attachInbox(request Request, batch InboxBatch) Request {
	if strings.TrimSpace(batch.Text) == "" {
		return request
	}
	request.Inputs = append(cloneInputs(request.Inputs), NewInboxInput(batch.Text))
	return request
}

func (e *Engine) acknowledgeInbox(batch InboxBatch) error {
	if e.inbox == nil || len(batch.MessageIDs) == 0 {
		return nil
	}
	return e.inbox.AcknowledgeInbox(batch)
}

func (e *Engine) settleInbox() bool {
	return e.inbox == nil || e.inbox.InboxEmpty()
}

func (e *Engine) generateResponse(ctx context.Context, request Request, sink EventSink) (Response, *toolEventTracker, bool, error) {
	retryPolicy, canRetry := e.provider.(GenerationRetryPolicy)
	for failedAttempts := 1; ; failedAttempts++ {
		toolEvents := newToolEventTracker(e.tools, sink)
		providerRequest := request
		providerRequest.Inputs = cloneInputs(request.Inputs)
		providerRequest.State = append([]byte(nil), request.State...)
		response, err := e.provider.Generate(ctx, providerRequest, StreamObserver{
			Text: func(text string) error {
				return toolEvents.emitProvider(Event{Kind: EventAssistantText, Text: text})
			},
			Reasoning: func(text string) error {
				return toolEvents.emitProvider(Event{Kind: EventAssistantReasoning, Text: text})
			},
			ToolCall: toolEvents.observeSnapshot,
		})
		if err == nil {
			return response, toolEvents, toolEvents.sawObserverEvent(), nil
		}

		observed := toolEvents.sawObserverEvent()
		closeErr := toolEvents.closeRemaining(err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, nil, observed, ctxErr
		}
		if closeErr != nil {
			return Response{}, nil, observed, closeErr
		}
		if !canRetry {
			return Response{}, nil, observed, newProviderGenerationError(err)
		}

		delay, retry := retryPolicy.RetryGeneration(err, failedAttempts)
		if !retry {
			return Response{}, nil, false, newProviderGenerationError(err)
		}
		if err := emit(sink, Event{Kind: EventGenerationRetry, Attempt: failedAttempts + 1}); err != nil {
			return Response{}, nil, false, err
		}
		if err := waitForRetry(ctx, delay); err != nil {
			return Response{}, nil, false, err
		}
	}
}

func newProviderGenerationError(err error) error {
	return &providerGenerationError{err: fmt.Errorf("agent: generate response: %w", err)}
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

func (e *Engine) compactSized(ctx context.Context, sink EventSink, compactRequest, sizingRequest Request, current conversationState) (Request, conversationState, Usage, error) {
	compactor, canCompact := e.provider.(Compactor)
	if !canCompact || !compactor.ShouldCompact(sizingRequest, current.usage) {
		return compactRequest, current, Usage{}, nil
	}

	return e.compactRequest(ctx, sink, compactor, compactRequest, current)
}

func (e *Engine) compactAfterError(ctx context.Context, sink EventSink, request Request, current conversationState, generationErr error) (Request, conversationState, Usage, bool, error) {
	compactor, canCompact := e.provider.(Compactor)
	policy, hasPolicy := e.provider.(CompactionErrorPolicy)
	if !canCompact || !hasPolicy || !policy.ShouldCompactAfterError(request, generationErr) {
		return request, current, Usage{}, false, generationErr
	}

	request, current, usage, err := e.compactRequest(ctx, sink, compactor, request, current)
	return request, current, usage, true, err
}

func (e *Engine) compactRequest(ctx context.Context, sink EventSink, compactor Compactor, request Request, current conversationState) (Request, conversationState, Usage, error) {
	if err := emit(sink, Event{Kind: EventCompactionStart}); err != nil {
		return request, current, Usage{}, err
	}

	providerRequest := request
	providerRequest.Inputs = cloneInputs(request.Inputs)
	providerRequest.State = append([]byte(nil), request.State...)
	compacted, err := compactor.Compact(ctx, providerRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return request, current, Usage{}, ctxErr
		}
		return request, current, Usage{}, fmt.Errorf("agent: compact context: %w", err)
	}
	if len(compacted.State) == 0 {
		return request, current, Usage{}, errors.New("agent: compact context: provider returned empty state")
	}

	current = conversationState{state: compacted.State}
	request.State = current.state
	request.Inputs = nil
	if err := emit(sink, Event{Kind: EventCompactionEnd, Usage: compacted.Usage}); err != nil {
		return request, current, compacted.Usage, err
	}
	if err := e.commitCheckpoint(current, sink); err != nil {
		return request, current, compacted.Usage, err
	}
	return request, current, compacted.Usage, nil
}
