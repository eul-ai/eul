package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const defaultMaxToolRounds = 20

var (
	errEngineBusy     = errors.New("agent: engine is busy")
	errToolRoundLimit = errors.New("agent: maximum tool rounds exceeded")
)

type Options struct {
	Model               string
	ThinkingLevel       ThinkingLevel
	WorkingDirectory    string
	ProjectInstructions string
	maxToolRounds       int
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
	maxToolRounds int
	instructions  string
	state         []byte
	contextUsage  Usage
	pendingInputs []Input
}

func New(provider Provider, tools Toolbox, options Options) *Engine {
	maxToolRounds := options.maxToolRounds
	if maxToolRounds == 0 {
		maxToolRounds = defaultMaxToolRounds
	}
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = DefaultThinkingLevel
	}

	return &Engine{
		provider:      provider,
		tools:         tools,
		model:         options.Model,
		thinkingLevel: thinkingLevel,
		maxToolRounds: maxToolRounds,
		instructions:  buildSystemPrompt(tools.Definitions(), options.WorkingDirectory, options.ProjectInstructions),
	}
}

func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	if !e.mu.TryLock() {
		return RunResult{}, errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	current := continuation{
		state:  e.state,
		usage:  e.contextUsage,
		inputs: append([]Input(nil), e.pendingInputs...),
	}
	current.inputs = append(current.inputs, Input{Kind: InputUser, Text: userText})
	var result RunResult
	toolRounds := 0

	for {
		if err := ctx.Err(); err != nil {
			current.checkpoint(e)
			return RunResult{}, err
		}

		request := e.request(current)
		var compactedUsage Usage
		var err error
		request, current, compactedUsage, err = e.compact(ctx, sink, request, current)
		addUsage(&result.Usage, compactedUsage)
		if err != nil {
			current.checkpoint(e)
			return RunResult{}, err
		}

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
		if err != nil {
			current.checkpoint(e)
			closeErr := toolEvents.closeRemaining(err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			if closeErr != nil {
				return RunResult{}, closeErr
			}
			return RunResult{}, fmt.Errorf("agent: generate response: %w", err)
		}

		responseContinuation := continuation{state: response.State, usage: response.Usage}
		addUsage(&result.Usage, response.Usage)
		if err := emit(sink, Event{Kind: EventContextUsage, Usage: response.Usage}); err != nil {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
			responseContinuation.checkpoint(e)
			return RunResult{}, err
		}
		if err := toolEvents.reconcileFinal(response.ToolCalls); err != nil {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, err)
			responseContinuation.checkpoint(e)
			return RunResult{}, err
		}

		if len(response.ToolCalls) == 0 {
			responseContinuation.checkpoint(e)
			result.Text = response.Text
			return result, nil
		}

		if toolRounds >= e.maxToolRounds {
			responseContinuation.inputs = unexecutedToolInputs(response.ToolCalls, errToolRoundLimit)
			responseContinuation.checkpoint(e)
			if err := toolEvents.closeRemaining(errToolRoundLimit); err != nil {
				return RunResult{}, err
			}
			return RunResult{}, errToolRoundLimit
		}
		toolRounds++

		inputs, err := e.executeToolRound(ctx, response.ToolCalls, toolEvents)
		responseContinuation.inputs = inputs
		current = responseContinuation
		if err != nil {
			current.checkpoint(e)
			return RunResult{}, err
		}
	}
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

func (e *Engine) compact(ctx context.Context, sink EventSink, request Request, current continuation) (Request, continuation, Usage, error) {
	compactor, canCompact := e.provider.(Compactor)
	if !canCompact || !compactor.ShouldCompact(request, current.usage) {
		return request, current, Usage{}, nil
	}

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
	return request, current, compacted.Usage, nil
}

func (e *Engine) executeToolRound(ctx context.Context, calls []ToolCall, toolEvents *toolEventTracker) ([]Input, error) {
	inputs := make([]Input, 0, len(calls))
	for callIndex, call := range calls {
		if ctxErr := ctx.Err(); ctxErr != nil {
			inputs = append(inputs, unexecutedToolInputs(calls[callIndex:], ctxErr)...)
			_ = toolEvents.closeRemaining(ctxErr)
			return inputs, ctxErr
		}

		if err := toolEvents.beginExecution(call); err != nil {
			inputs = append(inputs, unexecutedToolInputs(calls[callIndex:], err)...)
			return inputs, err
		}

		toolResult, err := e.executeTool(ctx, call, toolEvents.update(call))
		if updateErr := toolEvents.updateError(); updateErr != nil {
			toolResult = failedToolResult(call, toolResult, updateErr)
			inputs = append(inputs, toolResultInput(toolResult))
			inputs = append(inputs, unexecutedToolInputs(calls[callIndex+1:], updateErr)...)
			return inputs, updateErr
		}
		if err != nil {
			if toolResult.Output == "" {
				toolResult.Output = err.Error()
			}
			toolResult.IsError = true
			endErr := toolEvents.end(call, toolResult)
			closeErr := toolEvents.closeRemaining(err)
			inputs = append(inputs, toolResultInput(toolResult))
			inputs = append(inputs, unexecutedToolInputs(calls[callIndex+1:], err)...)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return inputs, ctxErr
			}
			if endErr != nil {
				return inputs, endErr
			}
			if closeErr != nil {
				return inputs, closeErr
			}
			return inputs, err
		}

		if err := toolEvents.end(call, toolResult); err != nil {
			inputs = append(inputs, toolResultInput(toolResult))
			inputs = append(inputs, unexecutedToolInputs(calls[callIndex+1:], err)...)
			return inputs, err
		}
		inputs = append(inputs, toolResultInput(toolResult))
	}
	return inputs, nil
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
