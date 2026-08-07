package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const defaultMaxToolRounds = 20

var (
	errToolRoundLimit = errors.New("agent: maximum tool rounds exceeded")
	errResetRequired  = errors.New("agent: reset required after incomplete tool turn")
)

type Options struct {
	Model               string
	ThinkingLevel       ThinkingLevel
	ProjectInstructions string
	maxToolRounds       int
}

type RunResult struct {
	Text  string
	Usage Usage
}

type Engine struct {
	provider      Provider
	tools         Toolbox
	model         string
	thinkingLevel ThinkingLevel
	maxToolRounds int
	instructions  string
	state         []byte
	contextUsage  Usage
	resetRequired bool
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
		instructions:  buildSystemPrompt(tools.Definitions(), options.ProjectInstructions),
	}
}

func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	if e.resetRequired {
		return RunResult{}, errResetRequired
	}

	state := e.state
	contextUsage := e.contextUsage
	inputs := []Input{{Kind: InputUser, Text: userText}}
	var result RunResult
	toolRounds := 0

	for {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}

		request := Request{
			Model:         e.model,
			ThinkingLevel: e.thinkingLevel,
			Instructions:  e.instructions,
			Inputs:        inputs,
			Tools:         e.tools.Definitions(),
			State:         state,
		}
		if compactor, canCompact := e.provider.(Compactor); canCompact && compactor.ShouldCompact(request, contextUsage) {
			if err := emit(sink, Event{Kind: EventCompactionStart}); err != nil {
				return RunResult{}, err
			}

			compacted, err := compactor.Compact(ctx, request)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return RunResult{}, ctxErr
				}
				return RunResult{}, fmt.Errorf("agent: compact context: %w", err)
			}
			if len(compacted.State) == 0 {
				return RunResult{}, errors.New("agent: compact context: provider returned empty state")
			}

			state = compacted.State
			inputs = nil
			addUsage(&result.Usage, compacted.Usage)
			request.State = state
			request.Inputs = nil

			if err := emit(sink, Event{Kind: EventCompactionEnd}); err != nil {
				return RunResult{}, err
			}
		}

		streamedTools := make(map[string]streamedTool)
		streamedOrder := make([]string, 0)
		var providerEventMu sync.Mutex
		emitProviderEvent := func(event Event) error {
			providerEventMu.Lock()
			defer providerEventMu.Unlock()
			return emit(sink, event)
		}
		response, err := e.provider.Generate(ctx, request, func(text string) error {
			return emitProviderEvent(Event{Kind: EventAssistantText, Text: text})
		}, func(text string) error {
			return emitProviderEvent(Event{Kind: EventAssistantReasoning, Text: text})
		}, func(snapshot ToolCallSnapshot) error {
			providerEventMu.Lock()
			defer providerEventMu.Unlock()

			if snapshot.ID == "" || snapshot.Name == "" {
				return nil
			}
			presentation := clonePresentation(e.tools.Presentation(snapshot))
			call := ToolCall{ID: snapshot.ID, Name: snapshot.Name, Arguments: []byte(snapshot.RawArguments)}
			current, exists := streamedTools[snapshot.ID]
			if exists && presentationsEqual(current.presentation, presentation) {
				current.call = call
				streamedTools[snapshot.ID] = current
				return nil
			}

			kind := EventToolUpdate
			if !exists {
				kind = EventToolStart
				streamedOrder = append(streamedOrder, snapshot.ID)
			}
			if err := emit(sink, Event{Kind: kind, Call: call, Presentation: presentation}); err != nil {
				return err
			}
			streamedTools[snapshot.ID] = streamedTool{call: call, presentation: presentation}
			return nil
		})
		if err != nil {
			closeErr := closeStreamedTools(sink, streamedTools, streamedOrder, err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunResult{}, ctxErr
			}
			if closeErr != nil {
				return RunResult{}, closeErr
			}
			return RunResult{}, fmt.Errorf("agent: generate response: %w", err)
		}

		state = response.State
		contextUsage = response.Usage
		addUsage(&result.Usage, response.Usage)
		if err := emit(sink, Event{Kind: EventContextUsage, Usage: response.Usage}); err != nil {
			return RunResult{}, err
		}

		finalCalls := make(map[string]struct{}, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			finalCalls[call.ID] = struct{}{}
		}
		for _, callID := range streamedOrder {
			streamed, exists := streamedTools[callID]
			if !exists {
				continue
			}
			if _, exists := finalCalls[callID]; exists {
				continue
			}
			result := ToolResult{CallID: callID, Tool: streamed.call.Name, Output: "tool call did not complete", IsError: true}
			if err := emit(sink, Event{Kind: EventToolEnd, Call: streamed.call, Presentation: streamed.presentation, Result: result}); err != nil {
				return RunResult{}, err
			}
			delete(streamedTools, callID)
		}

		if len(response.ToolCalls) == 0 {
			e.state = state
			e.contextUsage = contextUsage
			e.resetRequired = false
			result.Text = response.Text
			return result, nil
		}

		if toolRounds >= e.maxToolRounds {
			if err := closeStreamedTools(sink, streamedTools, streamedOrder, errToolRoundLimit); err != nil {
				return RunResult{}, err
			}
			return RunResult{}, errToolRoundLimit
		}
		toolRounds++

		inputs = make([]Input, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = closeStreamedTools(sink, streamedTools, streamedOrder, ctxErr)
				return RunResult{}, ctxErr
			}

			snapshot := completeToolCallSnapshot(call)
			presentation := clonePresentation(e.tools.Presentation(snapshot))
			streamed, exists := streamedTools[call.ID]
			if !exists {
				if err := emit(sink, Event{Kind: EventToolStart, Call: call, Presentation: presentation}); err != nil {
					return RunResult{}, err
				}
				streamed = streamedTool{call: call, presentation: presentation}
				streamedOrder = append(streamedOrder, call.ID)
			} else if !presentationsEqual(streamed.presentation, presentation) {
				if err := emit(sink, Event{Kind: EventToolUpdate, Call: call, Presentation: presentation}); err != nil {
					return RunResult{}, err
				}
				streamed.presentation = presentation
			}
			streamed.call = call
			streamedTools[call.ID] = streamed
			if err := emit(sink, Event{Kind: EventToolExecute, Call: call, Presentation: streamed.presentation}); err != nil {
				return RunResult{}, err
			}

			// A tool may change external state before failing or being canceled.
			// Reject another Run until a final provider response supplies coherent
			// continuation state or the caller invokes Reset.
			e.resetRequired = true
			var toolUpdateMu sync.Mutex
			var updateErr error
			toolResult, err := e.executeTool(ctx, call, func(next ToolPresentation) error {
				toolUpdateMu.Lock()
				defer toolUpdateMu.Unlock()
				if updateErr != nil {
					return updateErr
				}
				next = clonePresentation(next)
				if presentationsEqual(streamed.presentation, next) {
					return nil
				}
				if eventErr := emit(sink, Event{Kind: EventToolUpdate, Call: call, Presentation: next}); eventErr != nil {
					updateErr = eventErr
					return eventErr
				}
				streamed.presentation = next
				streamedTools[call.ID] = streamed
				return nil
			})
			toolUpdateMu.Lock()
			currentUpdateErr := updateErr
			streamed = streamedTools[call.ID]
			toolUpdateMu.Unlock()
			if currentUpdateErr != nil {
				return RunResult{}, currentUpdateErr
			}
			if err != nil {
				if toolResult.Output == "" {
					toolResult.Output = err.Error()
				}
				toolResult.IsError = true
				endErr := emit(sink, Event{Kind: EventToolEnd, Call: call, Presentation: streamed.presentation, Result: toolResult})
				delete(streamedTools, call.ID)
				closeErr := closeStreamedTools(sink, streamedTools, streamedOrder, err)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return RunResult{}, ctxErr
				}
				if endErr != nil {
					return RunResult{}, endErr
				}
				if closeErr != nil {
					return RunResult{}, closeErr
				}
				return RunResult{}, err
			}

			if err := emit(sink, Event{Kind: EventToolEnd, Call: call, Presentation: streamed.presentation, Result: toolResult}); err != nil {
				return RunResult{}, err
			}
			delete(streamedTools, call.ID)

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

func (e *Engine) SetThinkingLevel(level ThinkingLevel) error {
	if !level.Valid() {
		return errors.New("agent: invalid thinking level")
	}
	e.thinkingLevel = level
	return nil
}

func (e *Engine) NeedsReset() bool {
	return e.resetRequired
}

func (e *Engine) Reset() {
	e.state = nil
	e.contextUsage = Usage{}
	e.resetRequired = false
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

type streamedTool struct {
	call         ToolCall
	presentation ToolPresentation
}

func completeToolCallSnapshot(call ToolCall) ToolCallSnapshot {
	arguments := make(map[string]any)
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
		arguments = make(map[string]any)
	}
	return ToolCallSnapshot{
		ID:           call.ID,
		Name:         call.Name,
		RawArguments: string(call.Arguments),
		Arguments:    arguments,
		Complete:     true,
	}
}

func closeStreamedTools(sink EventSink, tools map[string]streamedTool, order []string, cause error) error {
	for _, callID := range order {
		streamed, exists := tools[callID]
		if !exists {
			continue
		}
		result := ToolResult{
			CallID:  streamed.call.ID,
			Tool:    streamed.call.Name,
			Output:  cause.Error(),
			IsError: true,
		}
		if err := emit(sink, Event{Kind: EventToolEnd, Call: streamed.call, Presentation: streamed.presentation, Result: result}); err != nil {
			return err
		}
		delete(tools, callID)
	}
	return nil
}

func clonePresentation(presentation ToolPresentation) ToolPresentation {
	presentation.Lines = append([]string(nil), presentation.Lines...)
	return presentation
}

func presentationsEqual(left, right ToolPresentation) bool {
	if left.Title != right.Title || left.Arguments != right.Arguments || left.Markdown != right.Markdown || left.Outcome != right.Outcome || len(left.Lines) != len(right.Lines) {
		return false
	}
	for index := range left.Lines {
		if left.Lines[index] != right.Lines[index] {
			return false
		}
	}
	return true
}

func emit(sink EventSink, event Event) error {
	return sink(event)
}

func addUsage(total *Usage, usage Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
}
