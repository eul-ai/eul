package agent

import (
	"context"
	"fmt"
)

type toolCompletion struct {
	index  int
	call   ToolCall
	result ToolResult
	err    error
}

func (t *turn) executeTools(calls []ToolCall) error {
	inputs, err := t.executeToolRound(calls)
	t.current.inputs = inputs
	if err != nil {
		return err
	}
	if err := t.commit(); err != nil {
		return err
	}

	next, ok := t.engine.continuations.nextAfterTools()
	if !ok {
		return nil
	}
	return t.deliver(next)
}

func (t *turn) executeToolRound(calls []ToolCall) ([]Input, error) {
	if err := t.beginToolRound(calls); err != nil {
		_ = t.tools.closeRemaining(err)
		return unexecutedToolInputs(calls, err), err
	}

	roundCtx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	steering := t.engine.continuations.beginToolRound()
	defer t.engine.continuations.endToolRound(steering)

	completions := make(chan toolCompletion, len(calls))
	for index, call := range calls {
		go func() {
			result, err := t.executeTool(context.WithValue(roundCtx, steeringSignalKey{}, steering), call)
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
		if err := t.tools.end(completion.call, completion.result); err != nil && roundErr == nil {
			roundErr = err
			cancel()
		}
	}

	inputs := make([]Input, len(results))
	for index, result := range results {
		inputs[index] = NewToolResultInput(result)
	}
	if err := t.ctx.Err(); err != nil {
		return inputs, err
	}
	return inputs, roundErr
}

func unexecutedToolInputs(calls []ToolCall, cause error) []Input {
	inputs := make([]Input, len(calls))
	for index, call := range calls {
		inputs[index] = NewToolResultInput(ToolResult{
			CallID:  call.ID,
			Tool:    call.Name,
			Output:  "tool was not executed: " + cause.Error(),
			IsError: true,
		})
	}
	return inputs
}

func (t *turn) beginToolRound(calls []ToolCall) error {
	for _, call := range calls {
		if err := t.tools.beginExecution(call); err != nil {
			return err
		}
	}
	return t.ctx.Err()
}

func (t *turn) executeTool(ctx context.Context, call ToolCall) (ToolResult, error) {
	tool, ok := t.engine.tools[call.Name]
	if !ok {
		return ToolResult{CallID: call.ID, Tool: call.Name, Output: fmt.Sprintf("unknown tool %q", call.Name), IsError: true}, nil
	}

	result, err := tool.Execute(ctx, call.Arguments, t.tools.update(call))
	switch {
	case err == nil:
	case ctx.Err() != nil:
		err = ctx.Err()
		result = failedToolResult(result, err)
	default:
		result, err = ToolResult{Output: err.Error(), IsError: true}, nil
	}
	if updateErr := t.tools.updateError(call); updateErr != nil {
		result, err = failedToolResult(result, updateErr), updateErr
	}

	result.CallID = call.ID
	result.Tool = call.Name
	return result, err
}

func failedToolResult(result ToolResult, cause error) ToolResult {
	if result.Output == "" {
		result.Output = cause.Error()
	}
	result.IsError = true
	return result
}
