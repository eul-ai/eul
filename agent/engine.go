package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var errEngineBusy = errors.New("agent: engine is busy")

type Options struct {
	Model               string
	ThinkingLevel       ThinkingLevel
	WorkingDirectory    string
	ProjectInstructions string
	Skills              []Skill
	Checkpointing       bool
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
	provider      Provider
	tools         Toolbox
	model         string
	thinkingLevel ThinkingLevel
	instructions  string
	state         []byte
	contextUsage  Usage
	pendingInputs []Input
	continuations continuationArbiter
	skills        map[string]Skill
	checkpointing bool
}

func New(provider Provider, tools Toolbox, options Options) *Engine {
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = DefaultThinkingLevel
	}
	skills := make(map[string]Skill, len(options.Skills))
	for _, skill := range options.Skills {
		skills[skill.Name] = skill
	}

	return &Engine{
		provider:      provider,
		tools:         tools,
		model:         options.Model,
		thinkingLevel: thinkingLevel,
		instructions:  buildSystemPrompt(tools.Definitions(), options.WorkingDirectory, options.ProjectInstructions, options.Skills),
		skills:        skills,
		checkpointing: options.Checkpointing,
	}
}

func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	return e.run(ctx, userText, sink, FinalizationPolicy{})
}

func (e *Engine) RunWithFinalization(ctx context.Context, userText string, sink EventSink, policy FinalizationPolicy) (RunResult, error) {
	return e.run(ctx, userText, sink, policy)
}

func (e *Engine) run(ctx context.Context, userText string, sink EventSink, policy FinalizationPolicy) (RunResult, error) {
	if !e.mu.TryLock() {
		return RunResult{}, errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	userText, err := expandSkillCommand(userText, e.skills)
	if err != nil {
		return RunResult{}, err
	}

	e.beginContinuations()
	defer e.endContinuations()

	current := continuation{
		state:  e.state,
		usage:  e.contextUsage,
		inputs: append([]Input(nil), e.pendingInputs...),
	}
	current.inputs = append(current.inputs, Input{Kind: InputUser, Text: userText})
	var result RunResult
	started := time.Now()
	normalGenerations := 0
	latestText := ""

	for {
		if err := ctx.Err(); err != nil {
			current.checkpoint(e)
			return RunResult{}, err
		}

		finalizationReason, finalizing := policy.shouldBegin(started, normalGenerations)
		if finalizing && policy.OnBegin != nil {
			policy.OnBegin(finalizationReason)
		}

		request := e.request(current)
		if finalizing {
			request.Tools = nil
			request.Instructions = appendFinalizationPrompt(request.Instructions, policy.Prompt)
		}
		var compactedUsage Usage
		var err error
		request, current, compactedUsage, err = e.compact(ctx, sink, request, current)
		addUsage(&result.Usage, compactedUsage)
		if err != nil {
			current.checkpoint(e)
			if finalizing {
				result.Text = latestText
				return result, err
			}
			return RunResult{}, err
		}
		if !finalizing {
			finalizationReason, finalizing = policy.shouldBegin(started, normalGenerations)
			if finalizing {
				if policy.OnBegin != nil {
					policy.OnBegin(finalizationReason)
				}
				request.Tools = nil
				request.Instructions = appendFinalizationPrompt(request.Instructions, policy.Prompt)
			}
		}

		generationSink := sink
		var finalText strings.Builder
		if finalizing {
			generationSink = func(event Event) error {
				if event.Kind == EventAssistantText {
					finalText.WriteString(event.Text)
				}
				return sink(event)
			}
		}

		response, toolEvents, err := e.generateResponse(ctx, request, generationSink)
		if err != nil {
			var compacted bool
			request, current, compactedUsage, compacted, err = e.compactAfterError(ctx, sink, request, current, err)
			addUsage(&result.Usage, compactedUsage)
			if err == nil && compacted {
				response, toolEvents, err = e.generateResponse(ctx, request, generationSink)
			}
		}
		if err != nil {
			current.checkpoint(e)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			if finalizing {
				result.Text = bestFinalizationText(finalText.String(), latestText)
				return result, err
			}
			return RunResult{}, err
		}

		responseContinuation := continuation{state: response.State, usage: response.Usage}
		addUsage(&result.Usage, response.Usage)
		if !finalizing {
			normalGenerations++
			if strings.TrimSpace(response.Text) != "" {
				latestText = response.Text
			}
		}
		if err := emit(sink, Event{Kind: EventContextUsage, Usage: response.Usage}); err != nil {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
			responseContinuation.checkpoint(e)
			return RunResult{}, err
		}
		if finalizing {
			if err := toolEvents.closeRemaining(errors.New("tools are disabled during finalization")); err != nil {
				return RunResult{}, err
			}
			if len(response.ToolCalls) == 0 {
				if err := e.commitCheckpoint(responseContinuation, sink); err != nil {
					return RunResult{}, err
				}
			}
			result.Text = bestFinalizationText(response.Text, finalText.String(), latestText)
			return result, nil
		}
		if err := toolEvents.reconcileFinal(response.ToolCalls); err != nil {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
			responseContinuation.checkpoint(e)
			return RunResult{}, err
		}

		if len(response.ToolCalls) == 0 {
			if err := e.commitCheckpoint(responseContinuation, sink); err != nil {
				return RunResult{}, err
			}

			next, ok := e.continuations.next(continuationBeforeSettle)
			if !ok {
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

func (e *Engine) request(current continuation) Request {
	return Request{
		Model:         e.model,
		ThinkingLevel: e.thinkingLevel,
		Instructions:  e.instructions,
		Inputs:        current.inputs,
		Tools:         e.tools.Definitions(),
		State:         current.state,
	}
}

func (e *Engine) generateResponse(ctx context.Context, request Request, sink EventSink) (Response, *toolEventTracker, error) {
	retryPolicy, canRetry := e.provider.(GenerationRetryPolicy)
	for failedAttempts := 1; ; failedAttempts++ {
		toolEvents := newToolEventTracker(e.tools, sink)
		response, err := e.provider.Generate(ctx, request, StreamObserver{
			Text: func(text string) error {
				return toolEvents.emitProvider(Event{Kind: EventAssistantText, Text: text})
			},
			Reasoning: func(text string) error {
				return toolEvents.emitProvider(Event{Kind: EventAssistantReasoning, Text: text})
			},
			ToolCall: toolEvents.observeSnapshot,
		})
		if err == nil {
			return response, toolEvents, nil
		}

		closeErr := toolEvents.closeRemaining(err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, nil, ctxErr
		}
		if closeErr != nil {
			return Response{}, nil, closeErr
		}
		if !canRetry {
			return Response{}, nil, fmt.Errorf("agent: generate response: %w", err)
		}

		delay, retry := retryPolicy.RetryGeneration(err, failedAttempts)
		if !retry {
			return Response{}, nil, fmt.Errorf("agent: generate response: %w", err)
		}
		if err := emit(sink, Event{Kind: EventGenerationRetry, Attempt: failedAttempts + 1}); err != nil {
			return Response{}, nil, err
		}
		if err := waitForRetry(ctx, delay); err != nil {
			return Response{}, nil, err
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

func (e *Engine) compact(ctx context.Context, sink EventSink, request Request, current continuation) (Request, continuation, Usage, error) {
	compactor, canCompact := e.provider.(Compactor)
	if !canCompact || !compactor.ShouldCompact(request, current.usage) {
		return request, current, Usage{}, nil
	}

	return e.compactRequest(ctx, sink, compactor, request, current)
}

func (e *Engine) compactAfterError(ctx context.Context, sink EventSink, request Request, current continuation, generationErr error) (Request, continuation, Usage, bool, error) {
	compactor, canCompact := e.provider.(Compactor)
	policy, hasPolicy := e.provider.(CompactionErrorPolicy)
	if !canCompact || !hasPolicy || !policy.ShouldCompactAfterError(request, generationErr) {
		return request, current, Usage{}, false, generationErr
	}

	request, current, usage, err := e.compactRequest(ctx, sink, compactor, request, current)
	return request, current, usage, true, err
}

func (e *Engine) compactRequest(ctx context.Context, sink EventSink, compactor Compactor, request Request, current continuation) (Request, continuation, Usage, error) {
	if err := emit(sink, Event{Kind: EventCompactionStart}); err != nil {
		return request, current, Usage{}, err
	}

	compacted, err := compactor.Compact(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return request, current, Usage{}, ctxErr
		}
		return request, current, Usage{}, fmt.Errorf("agent: compact context: %w", err)
	}
	if len(compacted.State) == 0 {
		return request, current, Usage{}, errors.New("agent: compact context: provider returned empty state")
	}

	current = continuation{state: compacted.State}
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

type continuation struct {
	state  []byte
	usage  Usage
	inputs []Input
}

func (current continuation) checkpoint(engine *Engine) {
	engine.state = append([]byte(nil), current.state...)
	engine.contextUsage = current.usage
	engine.pendingInputs = append([]Input(nil), current.inputs...)
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

	current := continuation{
		state:  e.state,
		usage:  e.contextUsage,
		inputs: append([]Input(nil), e.pendingInputs...),
	}
	if len(current.state) == 0 && len(current.inputs) == 0 {
		return errors.New("agent: no context to compact")
	}

	_, current, _, err := e.compactRequest(ctx, sink, compactor, e.request(current), current)
	current.checkpoint(e)
	return err
}

func (e *Engine) SetThinkingLevel(level ThinkingLevel) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	if !level.Valid() {
		return errors.New("agent: invalid thinking level")
	}
	e.thinkingLevel = level
	return nil
}

func (e *Engine) Reset() error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	e.state = nil
	e.contextUsage = Usage{}
	e.pendingInputs = nil
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

func deliverContinuation(current *continuation, next pendingContinuation, sink EventSink) error {
	current.inputs = append(current.inputs, Input{Kind: InputUser, Text: next.text})
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
