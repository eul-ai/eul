package agent

import (
	"context"
	"errors"
	"fmt"
)

const defaultMaxToolRounds = 20

var (
	errToolRoundLimit = errors.New("agent: maximum tool rounds exceeded")
	errResetRequired  = errors.New("agent: reset required after incomplete tool turn")
)

// Options configures an Engine.
type Options struct {
	Model         string
	maxToolRounds int
}

// RunResult is the completed result of one user turn.
type RunResult struct {
	// Text is the final, tool-free assistant response.
	Text  string
	Usage Usage
}

// Engine owns provider conversation state and the provider/tool-call loop.
type Engine struct {
	provider      Provider
	tools         Toolbox
	model         string
	maxToolRounds int
	instructions  string
	state         []byte
	resetRequired bool
}

// New constructs an Engine from its provider and tools.
func New(provider Provider, tools Toolbox, options Options) *Engine {
	maxToolRounds := options.maxToolRounds
	if maxToolRounds == 0 {
		maxToolRounds = defaultMaxToolRounds
	}

	return &Engine{
		provider:      provider,
		tools:         tools,
		model:         options.Model,
		maxToolRounds: maxToolRounds,
		instructions:  buildSystemPrompt(tools.Definitions()),
	}
}

// Run processes one user turn.
func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	if e.resetRequired {
		return RunResult{}, errResetRequired
	}

	state := e.state
	inputs := []Input{{Kind: InputUser, Text: userText}}
	var result RunResult
	toolRounds := 0

	for {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}

		request := Request{
			Model:        e.model,
			Instructions: e.instructions,
			Inputs:       inputs,
			Tools:        e.tools.Definitions(),
			State:        state,
		}

		response, err := e.provider.Generate(ctx, request, func(text string) error {
			return emit(sink, Event{Kind: EventAssistantText, Text: text})
		}, func(text string) error {
			return emit(sink, Event{Kind: EventAssistantReasoning, Text: text})
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			return RunResult{}, fmt.Errorf("agent: generate response: %w", err)
		}

		state = response.State
		addUsage(&result.Usage, response.Usage)

		if len(response.ToolCalls) == 0 {
			e.state = state
			e.resetRequired = false
			result.Text = response.Text
			return result, nil
		}

		if toolRounds >= e.maxToolRounds {
			return RunResult{}, errToolRoundLimit
		}
		toolRounds++

		inputs = make([]Input, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			if err := ctx.Err(); err != nil {
				return RunResult{}, err
			}
			if err := emit(sink, Event{Kind: EventToolStart, Call: call}); err != nil {
				return RunResult{}, err
			}

			// Once execution starts, the external world may have changed even if
			// the call later fails or is canceled. Require Reset until a final
			// provider response commits a coherent continuation state.
			e.resetRequired = true
			toolResult, err := e.executeTool(ctx, call)
			if err != nil {
				return RunResult{}, err
			}
			if err := emit(sink, Event{Kind: EventToolEnd, Call: call, Result: toolResult}); err != nil {
				return RunResult{}, err
			}

			inputs = append(inputs, Input{
				Kind:    InputToolResult,
				Text:    toolResult.Output,
				CallID:  toolResult.CallID,
				Tool:    toolResult.Tool,
				IsError: toolResult.IsError,
			})
		}
	}
}

// NeedsReset reports whether an incomplete tool turn requires Reset before the
// next Run. Call it after Run has returned.
func (e *Engine) NeedsReset() bool {
	return e.resetRequired
}

// Reset discards provider continuation state.
func (e *Engine) Reset() {
	e.state = nil
	e.resetRequired = false
}

func (e *Engine) executeTool(ctx context.Context, call ToolCall) (ToolResult, error) {
	result, err := e.tools.Execute(ctx, call)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ToolResult{}, ctxErr
		}
		result = ToolResult{Output: err.Error(), IsError: true}
	}

	result.CallID = call.ID
	result.Tool = call.Name
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
