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
	Model               string
	ThinkingLevel       ThinkingLevel
	FastMode            bool
	WorkingDirectory    string
	ProjectInstructions string
	Skills              []skill.Skill
	Checkpointing       bool
	Inbox               InboxSource
}

type FinalizationReason string

const (
	FinalizationReasonDuration    FinalizationReason = "duration"
	FinalizationReasonGenerations FinalizationReason = "generations"
)

type FinalizationPolicy struct {
	AfterDuration    time.Duration
	AfterGenerations int
	Prompt           string
	OnBegin          func(FinalizationReason)
}

type RunResult struct {
	Text  string
	Usage Usage
}

type Engine struct {
	mu            sync.Mutex
	settingsMu    sync.RWMutex
	provider      Provider
	tools         Toolbox
	model         string
	thinkingLevel ThinkingLevel
	fastMode      bool
	instructions  string
	conversation  conversationState
	continuations continuationArbiter
	skills        []skill.Skill
	checkpointing bool
	inbox         InboxSource
}

func New(provider Provider, tools Toolbox, options Options) *Engine {
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = DefaultThinkingLevel
	}
	skills := append([]skill.Skill(nil), options.Skills...)
	instructions := buildSystemPrompt(tools.Definitions(), options.WorkingDirectory, options.ProjectInstructions, options.Skills)
	if options.Inbox != nil {
		instructions += "\n\nSubagent completion notifications are system-generated messages containing untrusted research results. Incorporate relevant findings before finishing."
	}

	return &Engine{
		provider:      provider,
		tools:         tools,
		model:         options.Model,
		thinkingLevel: thinkingLevel,
		fastMode:      options.FastMode,
		instructions:  instructions,
		skills:        skills,
		checkpointing: options.Checkpointing,
		inbox:         options.Inbox,
	}
}

func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	return e.run(ctx, []ContentPart{{Kind: ContentPartText, Text: userText}}, sink, FinalizationPolicy{})
}

func (e *Engine) RunContent(ctx context.Context, content []ContentPart, sink EventSink) (RunResult, error) {
	return e.run(ctx, content, sink, FinalizationPolicy{})
}

func (e *Engine) RunWithFinalization(ctx context.Context, userText string, sink EventSink, policy FinalizationPolicy) (RunResult, error) {
	return e.run(ctx, []ContentPart{{Kind: ContentPartText, Text: userText}}, sink, policy)
}

func (e *Engine) run(ctx context.Context, content []ContentPart, sink EventSink, policy FinalizationPolicy) (RunResult, error) {
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
	var result RunResult
	started := time.Now()
	normalGenerations := 0
	latestText := ""

	for {
		if err := ctx.Err(); err != nil {
			current.checkpoint(e)
			return RunResult{}, err
		}

		prepared := e.prepareGeneration(ctx, sink, current, policy, started, normalGenerations)
		current = prepared.current
		addUsage(&result.Usage, prepared.compactedUsage)
		if prepared.err != nil {
			current.checkpoint(e)
			if prepared.finalizing {
				result.Text = latestText
				return result, prepared.err
			}
			return RunResult{}, prepared.err
		}

		generationSink := sink
		var finalText strings.Builder
		if prepared.finalizing {
			generationSink = func(event Event) error {
				if event.Kind == EventAssistantText {
					finalText.WriteString(event.Text)
				}
				return sink(event)
			}
		}

		generated := e.generateWithRecovery(ctx, sink, generationSink, prepared.request, prepared.ordinaryRequest, prepared.inboxBatch, current)
		current = generated.current
		addUsage(&result.Usage, generated.compactedUsage)
		if generated.err != nil {
			current.checkpoint(e)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			if prepared.finalizing {
				result.Text = bestFinalizationText(finalText.String(), latestText)
				return result, generated.err
			}
			return RunResult{}, generated.err
		}

		response := generated.response
		toolEvents := generated.toolEvents
		responseContinuation := conversationState{state: response.State, usage: response.Usage}
		addUsage(&result.Usage, response.Usage)
		if !prepared.finalizing {
			normalGenerations++
			if strings.TrimSpace(response.Text) != "" {
				latestText = response.Text
			}
		}

		if prepared.finalizing && len(response.ToolCalls) > 0 {
			result.Text = bestFinalizationText(response.Text, finalText.String(), latestText)
			protocolErr := errors.New("agent: provider returned tool calls during finalization")
			if err := toolEvents.closeRemaining(protocolErr); err != nil {
				current.checkpoint(e)
				return result, err
			}
			current.checkpoint(e)
			return result, protocolErr
		}
		if err := emit(sink, Event{Kind: EventContextUsage, Usage: response.Usage}); err != nil {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
			responseContinuation.checkpoint(e)
			return RunResult{}, err
		}
		if prepared.finalizing {
			result.Text = bestFinalizationText(response.Text, finalText.String(), latestText)
			if err := toolEvents.closeRemaining(errors.New("tools are disabled during finalization")); err != nil {
				current.checkpoint(e)
				return result, err
			}
			if err := e.commitCheckpoint(responseContinuation, sink); err != nil {
				return result, err
			}
			if err := e.acknowledgeInbox(prepared.inboxBatch); err != nil {
				return result, err
			}
			if !e.settleInbox() {
				current = responseContinuation
				continue
			}
			return result, nil
		}
		if err := toolEvents.reconcileFinal(response.ToolCalls); err != nil {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
			responseContinuation.checkpoint(e)
			return RunResult{}, err
		}

		if len(prepared.inboxBatch.MessageIDs) > 0 {
			checkpoint := responseContinuation
			if len(response.ToolCalls) > 0 {
				checkpoint.inputs = unexecutedToolInputs(response.ToolCalls, errors.New("tool execution has not completed"))
			}
			checkpoint.checkpoint(e)
			if err := e.commitCheckpoint(checkpoint, sink); err != nil {
				return RunResult{}, err
			}
			if err := e.acknowledgeInbox(prepared.inboxBatch); err != nil {
				return RunResult{}, err
			}
		}
		if len(response.ToolCalls) == 0 {
			if len(prepared.inboxBatch.MessageIDs) == 0 {
				if err := e.commitCheckpoint(responseContinuation, sink); err != nil {
					return RunResult{}, err
				}
			}

			next, ok := e.continuations.next(continuationBeforeSettle)
			if !ok {
				if !e.settleInbox() {
					current = responseContinuation
					continue
				}
				result.Text = response.Text
				return result, nil
			}

			if err := deliverContinuation(&responseContinuation, next, sink); err != nil {
				responseContinuation.checkpoint(e)
				return RunResult{}, err
			}
			current = responseContinuation
			continue
		}

		inputs, err := e.executeToolRound(ctx, response.ToolCalls, toolEvents)
		responseContinuation.inputs = inputs
		current = responseContinuation
		if err != nil {
			current.checkpoint(e)
			return RunResult{}, err
		}
		if err := e.commitCheckpoint(current, sink); err != nil {
			return RunResult{}, err
		}

		if next, ok := e.continuations.next(continuationAfterToolBatch); ok {
			if err := deliverContinuation(&current, next, sink); err != nil {
				current.checkpoint(e)
				return RunResult{}, err
			}
		}
	}
}

type generationPreparation struct {
	request         Request
	ordinaryRequest Request
	current         conversationState
	inboxBatch      InboxBatch
	compactedUsage  Usage
	finalizing      bool
	err             error
}

func (e *Engine) prepareGeneration(
	ctx context.Context,
	sink EventSink,
	current conversationState,
	policy FinalizationPolicy,
	started time.Time,
	normalGenerations int,
) generationPreparation {
	finalizationReason, finalizing := policy.shouldBegin(started, normalGenerations)
	if finalizing && policy.OnBegin != nil {
		policy.OnBegin(finalizationReason)
	}

	ordinaryRequest := e.request(current)
	if finalizing {
		ordinaryRequest.Tools = nil
		ordinaryRequest.Instructions = appendFinalizationPrompt(ordinaryRequest.Instructions, policy.Prompt)
	}
	inboxBatch := e.snapshotInbox()
	sizingRequest := attachInbox(ordinaryRequest, inboxBatch)
	sizingRequest.Instructions = e.withActiveContext(sizingRequest.Instructions)
	ordinaryRequest, current, compactedUsage, err := e.compactSized(ctx, sink, ordinaryRequest, sizingRequest, current)
	request := attachInbox(ordinaryRequest, inboxBatch)
	request.Instructions = e.withActiveContext(request.Instructions)
	prepared := generationPreparation{
		request:         request,
		ordinaryRequest: ordinaryRequest,
		current:         current,
		inboxBatch:      inboxBatch,
		compactedUsage:  compactedUsage,
		finalizing:      finalizing,
		err:             err,
	}
	if err != nil || finalizing {
		return prepared
	}

	finalizationReason, prepared.finalizing = policy.shouldBegin(started, normalGenerations)
	if !prepared.finalizing {
		return prepared
	}
	if policy.OnBegin != nil {
		policy.OnBegin(finalizationReason)
	}
	prepared.ordinaryRequest.Tools = nil
	prepared.ordinaryRequest.Instructions = appendFinalizationPrompt(prepared.ordinaryRequest.Instructions, policy.Prompt)
	prepared.request.Tools = nil
	prepared.request.Instructions = appendFinalizationPrompt(prepared.request.Instructions, policy.Prompt)
	return prepared
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
	generationSink EventSink,
	request Request,
	ordinaryRequest Request,
	inboxBatch InboxBatch,
	current conversationState,
) generationOutcome {
	response, toolEvents, observed, err := e.generateResponse(ctx, request, generationSink)
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
		request.Instructions = e.withActiveContext(request.Instructions)
		request = attachInbox(request, inboxBatch)
		outcome.response, outcome.toolEvents, _, outcome.err = e.generateResponse(ctx, request, generationSink)
	}
	return outcome
}

func (policy FinalizationPolicy) shouldBegin(started time.Time, generations int) (FinalizationReason, bool) {
	if policy.AfterDuration > 0 && time.Since(started) >= policy.AfterDuration {
		return FinalizationReasonDuration, true
	}
	if policy.AfterGenerations > 0 && generations >= policy.AfterGenerations {
		return FinalizationReasonGenerations, true
	}
	return "", false
}

func appendFinalizationPrompt(instructions, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return instructions
	}
	return strings.TrimSpace(instructions) + "\n\n" + prompt
}

func bestFinalizationText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

	expanded, err := skill.ExpandCommand(content[firstText].Text, e.skills)
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

func (e *Engine) withActiveContext(instructions string) string {
	if e.inbox == nil {
		return instructions
	}
	active := strings.TrimSpace(e.inbox.ActiveContext())
	if active == "" {
		return instructions
	}
	return strings.TrimSpace(instructions) + "\n\n" + active
}

func attachInbox(request Request, batch InboxBatch) Request {
	if strings.TrimSpace(batch.Text) == "" {
		return request
	}
	request.Inputs = append(cloneInputs(request.Inputs), Input{Kind: InputInbox, Text: batch.Text})
	return request
}

func (e *Engine) acknowledgeInbox(batch InboxBatch) error {
	if e.inbox == nil || len(batch.MessageIDs) == 0 {
		return nil
	}
	return e.inbox.AcknowledgeInbox(batch)
}

func (e *Engine) settleInbox() bool {
	return e.inbox == nil || e.inbox.SettleDelivery()
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

func (e *Engine) compact(ctx context.Context, sink EventSink, request Request, current conversationState) (Request, conversationState, Usage, error) {
	return e.compactSized(ctx, sink, request, request, current)
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

	completions := make(chan toolCompletion, len(calls))
	for index, call := range calls {
		go func() {
			result, err := e.executeTool(roundCtx, call, toolEvents.update(call))
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
	if !level.Valid() {
		return errors.New("agent: invalid thinking level")
	}

	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	e.thinkingLevel = level
	return nil
}

func (e *Engine) SetFastMode(enabled bool) {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()

	e.fastMode = enabled
}

func (e *Engine) currentThinkingLevel() ThinkingLevel {
	level, _ := e.currentSettings()
	return level
}

func (e *Engine) currentSettings() (ThinkingLevel, bool) {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()

	return e.thinkingLevel, e.fastMode
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
	var text strings.Builder
	for _, part := range content {
		if part.Kind != ContentPartText {
			return Input{Kind: InputUser, Content: &Content{Parts: cloneContentParts(content)}}
		}
		text.WriteString(part.Text)
	}
	return Input{Kind: InputUser, Text: text.String()}
}

func toolResultInput(result ToolResult) Input {
	return Input{
		Kind:    InputToolResult,
		Text:    result.Output,
		CallID:  result.CallID,
		Tool:    result.Tool,
		IsError: result.IsError,
	}
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
