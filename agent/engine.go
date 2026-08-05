package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

const DefaultMaxToolRounds = 20

var (
	// ErrToolRoundLimit is returned when a model requests more tool rounds than
	// the engine permits for one user turn.
	ErrToolRoundLimit = errors.New("agent: maximum tool rounds exceeded")

	// ErrInvalidToolCall is returned for provider responses whose tool calls
	// cannot be correlated safely.
	ErrInvalidToolCall = errors.New("agent: invalid tool call")

	// ErrResetRequired is returned when a prior turn stopped after tool
	// execution began. Reset must be called before the conversation can safely
	// continue.
	ErrResetRequired = errors.New("agent: reset required after incomplete tool turn")
)

// Options configures an Engine.
type Options struct {
	Model         string
	MaxToolRounds int
}

// RunResult is the completed result of one user turn.
type RunResult struct {
	// Text is the final, tool-free assistant response.
	Text string
	// AssistantMessages preserves text from every provider response in the
	// turn, including responses that also requested tools.
	AssistantMessages []string
	ToolResults       []ToolResult
	Usage             Usage
}

// Engine owns provider conversation state and the provider/tool-call loop.
type Engine struct {
	runGate chan struct{}

	provider      Provider
	tools         Toolbox
	model         string
	maxToolRounds int
	definitions   []ToolDefinition
	instructions  string
	state         []byte
	resetRequired bool
}

// New constructs an Engine from its consumer-owned provider and tool seams.
func New(provider Provider, tools Toolbox, options Options) (*Engine, error) {
	if provider == nil {
		return nil, errors.New("agent: provider is required")
	}
	if tools == nil {
		return nil, errors.New("agent: toolbox is required")
	}
	if options.MaxToolRounds < 0 {
		return nil, errors.New("agent: maximum tool rounds cannot be negative")
	}

	maxToolRounds := options.MaxToolRounds
	if maxToolRounds == 0 {
		maxToolRounds = DefaultMaxToolRounds
	}

	definitions := cloneDefinitions(tools.Definitions())
	slices.SortFunc(definitions, compareToolDefinitions)
	for i, definition := range definitions {
		if definition.Name == "" {
			return nil, errors.New("agent: tool definition name is required")
		}
		if i > 0 && definitions[i-1].Name == definition.Name {
			return nil, fmt.Errorf("agent: duplicate tool definition %q", definition.Name)
		}
	}

	return &Engine{
		runGate:       make(chan struct{}, 1),
		provider:      provider,
		tools:         tools,
		model:         options.Model,
		maxToolRounds: maxToolRounds,
		definitions:   definitions,
		instructions:  BuildSystemPrompt(definitions),
	}, nil
}

// Run processes one user turn. Calls on the same Engine are serialized so its
// provider continuation state remains ordered.
func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, errors.New("agent: context is required")
	}

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	select {
	case e.runGate <- struct{}{}:
		defer func() { <-e.runGate }()
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	if e.resetRequired {
		return RunResult{}, ErrResetRequired
	}

	state := slices.Clone(e.state)
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
			Inputs:       slices.Clone(inputs),
			Tools:        cloneDefinitions(e.definitions),
			State:        slices.Clone(state),
		}

		response, err := e.provider.Generate(ctx, request, func(text string) error {
			return emit(sink, Event{Kind: EventAssistantText, Text: text})
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			return RunResult{}, fmt.Errorf("agent: generate response: %w", err)
		}

		state = slices.Clone(response.State)
		addUsage(&result.Usage, response.Usage)
		if response.Text != "" {
			result.AssistantMessages = append(result.AssistantMessages, response.Text)
		}

		if len(response.ToolCalls) == 0 {
			e.state = state
			e.resetRequired = false
			result.Text = response.Text
			return result, nil
		}

		if err := validateToolCalls(response.ToolCalls); err != nil {
			return RunResult{}, err
		}
		if toolRounds >= e.maxToolRounds {
			return RunResult{}, ErrToolRoundLimit
		}
		toolRounds++

		inputs = make([]Input, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			if err := ctx.Err(); err != nil {
				return RunResult{}, err
			}
			if err := emit(sink, Event{Kind: EventToolStart, Call: cloneToolCall(call)}); err != nil {
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
			if err := emit(sink, Event{Kind: EventToolEnd, Call: cloneToolCall(call), Result: toolResult}); err != nil {
				return RunResult{}, err
			}

			result.ToolResults = append(result.ToolResults, toolResult)
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

// Reset discards provider continuation state.
func (e *Engine) Reset() {
	e.runGate <- struct{}{}
	defer func() { <-e.runGate }()
	e.state = nil
	e.resetRequired = false
}

func (e *Engine) executeTool(ctx context.Context, call ToolCall) (ToolResult, error) {
	if !json.Valid(call.Arguments) {
		return ToolResult{
			CallID:  call.ID,
			Tool:    call.Name,
			Output:  "invalid tool arguments: malformed JSON",
			IsError: true,
		}, nil
	}

	result, err := e.tools.Execute(ctx, cloneToolCall(call))
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

func validateToolCalls(calls []ToolCall) error {
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.ID == "" {
			return fmt.Errorf("%w: missing call ID", ErrInvalidToolCall)
		}
		if _, exists := seen[call.ID]; exists {
			return fmt.Errorf("%w: duplicate call ID %q", ErrInvalidToolCall, call.ID)
		}
		seen[call.ID] = struct{}{}
	}
	return nil
}

func emit(sink EventSink, event Event) error {
	if sink == nil {
		return nil
	}
	return sink(event)
}

func addUsage(total *Usage, usage Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
}

func cloneToolCall(call ToolCall) ToolCall {
	call.Arguments = slices.Clone(call.Arguments)
	return call
}

func cloneDefinitions(definitions []ToolDefinition) []ToolDefinition {
	cloned := make([]ToolDefinition, len(definitions))
	for i, definition := range definitions {
		definition.PromptGuidelines = slices.Clone(definition.PromptGuidelines)
		definition.Parameters = cloneSchema(definition.Parameters)
		cloned[i] = definition
	}
	return cloned
}

func cloneSchema(schema JSONSchema) JSONSchema {
	schema.Required = slices.Clone(schema.Required)
	if schema.AdditionalProperties != nil {
		value := *schema.AdditionalProperties
		schema.AdditionalProperties = &value
	}
	if schema.Items != nil {
		items := cloneSchema(*schema.Items)
		schema.Items = &items
	}
	if schema.AnyOf != nil {
		anyOf := schema.AnyOf
		schema.AnyOf = make([]JSONSchema, len(anyOf))
		for i, item := range anyOf {
			schema.AnyOf[i] = cloneSchema(item)
		}
	}
	if schema.Properties != nil {
		properties := schema.Properties
		schema.Properties = make(map[string]JSONSchema, len(properties))
		for name, property := range properties {
			schema.Properties[name] = cloneSchema(property)
		}
	}
	return schema
}
