package agent

import "context"

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
