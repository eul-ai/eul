package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/skill"
)

var errEngineBusy = errors.New("agent: engine is busy")

type Options struct {
	Model                  string
	WorkingDirectory       string
	ProjectInstructions    string
	Skills                 []skill.Skill
	Checkpointing          bool
	Inbox                  InboxSource
	AdditionalInstructions func() string
	Settings               *Settings
}

type RunResult struct {
	Text  string
	Usage Usage
}

type Engine struct {
	mu                     sync.Mutex
	provider               Provider
	tools                  Toolbox
	model                  string
	settings               *Settings
	instructions           string
	conversation           conversationState
	continuations          continuationArbiter
	skills                 []skill.Skill
	checkpointing          bool
	inbox                  InboxSource
	additionalInstructions func() string
}

func New(provider Provider, tools Toolbox, options Options) *Engine {
	settings := options.Settings
	if settings == nil {
		settings = NewSettings(DefaultThinkingLevel, false)
	}
	skills := append([]skill.Skill(nil), options.Skills...)
	instructions := buildSystemPrompt(tools.Definitions(), options.WorkingDirectory, options.ProjectInstructions, options.Skills)

	return &Engine{
		provider:               provider,
		tools:                  tools,
		model:                  options.Model,
		settings:               settings,
		instructions:           instructions,
		skills:                 skills,
		checkpointing:          options.Checkpointing,
		inbox:                  options.Inbox,
		additionalInstructions: options.AdditionalInstructions,
	}
}

func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	return e.run(ctx, []ContentPart{{Kind: ContentPartText, Text: userText}}, sink)
}

func (e *Engine) RunContent(ctx context.Context, content []ContentPart, sink EventSink) (RunResult, error) {
	return e.run(ctx, content, sink)
}

func (e *Engine) run(ctx context.Context, content []ContentPart, sink EventSink) (RunResult, error) {
	if !e.mu.TryLock() {
		return RunResult{}, errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	content, err := e.expandSkillContent(content)
	if err != nil {
		return RunResult{}, err
	}

	e.beginContinuations()
	defer e.endContinuations()

	current := e.conversation.clone()
	current.inputs = append(current.inputs, userInput(content))
	return (&engineTurn{
		engine:  e,
		ctx:     ctx,
		sink:    sink,
		current: current,
	}).run()
}

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

type toolCompletion struct {
	index  int
	call   ToolCall
	result ToolResult
	err    error
}

func (e *Engine) executeToolRound(ctx context.Context, calls []ToolCall, toolEvents *toolEventTracker) ([]Input, error) {
	if err := ctx.Err(); err != nil {
		_ = toolEvents.closeRemaining(err)
		return unexecutedToolInputs(calls, err), err
	}

	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			_ = toolEvents.closeRemaining(err)
			return unexecutedToolInputs(calls, err), err
		}
		if err := toolEvents.beginExecution(call); err != nil {
			_ = toolEvents.closeRemaining(err)
			return unexecutedToolInputs(calls, err), err
		}
	}

	if err := ctx.Err(); err != nil {
		_ = toolEvents.closeRemaining(err)
		return unexecutedToolInputs(calls, err), err
	}

	roundCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	steering := e.continuations.beginToolRound()
	defer e.continuations.endToolRound(steering)

	completions := make(chan toolCompletion, len(calls))
	for index, call := range calls {
		go func() {
			toolCtx := context.WithValue(roundCtx, steeringSignalKey{}, steering)
			result, err := e.executeTool(toolCtx, call, toolEvents.update(call))
			if updateErr := toolEvents.updateError(call); updateErr != nil {
				result = failedToolResult(call, result, updateErr)
				err = updateErr
			}
			if err != nil {
				result = failedToolResult(call, result, err)
			}
			completions <- toolCompletion{index: index, call: call, result: result, err: err}
		}()
	}

	results := make([]ToolResult, len(calls))
	var roundErr error
	for range calls {
		completion := <-completions
		results[completion.index] = completion.result

		if completion.err != nil && roundErr == nil {
			roundErr = completion.err
			cancel()
		}
		if err := toolEvents.end(completion.call, completion.result); err != nil && roundErr == nil {
			roundErr = err
			cancel()
		}
	}

	inputs := make([]Input, len(results))
	for index, result := range results {
		inputs[index] = toolResultInput(result)
	}
	if err := ctx.Err(); err != nil {
		return inputs, err
	}
	return inputs, roundErr
}

type conversationState struct {
	state  []byte
	usage  Usage
	inputs []Input
}

func (current conversationState) clone() conversationState {
	current.state = append([]byte(nil), current.state...)
	current.inputs = cloneInputs(current.inputs)
	return current
}

func (current conversationState) checkpoint(engine *Engine) {
	engine.conversation = current.clone()
}

func (e *Engine) Compact(ctx context.Context, sink EventSink) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	compactor, canCompact := e.provider.(Compactor)
	if !canCompact {
		return errors.New("agent: context compaction is unavailable")
	}

	current := e.conversation.clone()
	if len(current.state) == 0 && len(current.inputs) == 0 {
		return errors.New("agent: no context to compact")
	}

	_, current, _, err := e.compactRequest(ctx, sink, compactor, e.request(current), current)
	current.checkpoint(e)
	return err
}

func (e *Engine) SetThinkingLevel(level ThinkingLevel) error {
	return e.settings.SetThinkingLevel(level)
}

func (e *Engine) SetFastMode(enabled bool) {
	e.settings.SetFastMode(enabled)
}

func (e *Engine) currentThinkingLevel() ThinkingLevel {
	level, _ := e.currentSettings()
	return level
}

func (e *Engine) currentSettings() (ThinkingLevel, bool) {
	return e.settings.Snapshot()
}

func (e *Engine) Reset() error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	e.conversation = conversationState{}
	e.continuations.reset()
	return nil
}

func (e *Engine) Steer(text string) bool {
	return e.continuations.steer(text)
}

func (e *Engine) ClearSteering() []string {
	return e.continuations.clearSteering()
}

func (e *Engine) SetGoal(objective string) error {
	return e.continuations.setGoal(objective)
}

func (e *Engine) Goal() (GoalState, bool) {
	return e.continuations.getGoal()
}

func (e *Engine) ClearGoal() {
	e.continuations.clearGoal()
}

func (e *Engine) CompleteGoal() error {
	return e.continuations.completeGoal()
}

func (e *Engine) beginContinuations() {
	e.continuations.beginRun()
}

func (e *Engine) endContinuations() {
	e.continuations.endRun()
}

func deliverContinuation(current *conversationState, next pendingContinuation, sink EventSink) error {
	current.inputs = append(current.inputs, userInput([]ContentPart{{Kind: ContentPartText, Text: next.text}}))
	eventKind := EventSteering
	if next.kind == continuationGoal {
		eventKind = EventGoalContinuation
	}
	if err := emit(sink, Event{Kind: eventKind, Text: next.text}); err != nil {
		current.inputs = current.inputs[:len(current.inputs)-1]
		return err
	}
	return nil
}

func userInput(content []ContentPart) Input {
	return NewUserInput(content...)
}

func toolResultInput(result ToolResult) Input {
	return NewToolResultInput(result)
}

func unexecutedToolInputs(calls []ToolCall, cause error) []Input {
	inputs := make([]Input, len(calls))
	for index, call := range calls {
		inputs[index] = toolResultInput(ToolResult{
			CallID:  call.ID,
			Tool:    call.Name,
			Output:  "tool was not executed: " + cause.Error(),
			IsError: true,
		})
	}
	return inputs
}

func failedToolResult(call ToolCall, result ToolResult, cause error) ToolResult {
	result.CallID = call.ID
	result.Tool = call.Name
	if result.Output == "" {
		result.Output = cause.Error()
	}
	result.IsError = true
	return result
}

func (e *Engine) executeTool(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error) {
	result, err := e.tools.Execute(ctx, call, updates)
	result.CallID = call.ID
	result.Tool = call.Name
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if result.Output == "" {
				result.Output = ctxErr.Error()
			}
			result.IsError = true
			return result, ctxErr
		}
		result = ToolResult{CallID: call.ID, Tool: call.Name, Output: err.Error(), IsError: true}
	}
	return result, nil
}

func emit(sink EventSink, event Event) error {
	return sink(event)
}

func addUsage(total *Usage, usage Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
}
