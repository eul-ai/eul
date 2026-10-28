package agent

import "context"

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

	next, ok := t.engine.continuations.next(continuationAfterToolBatch)
	if !ok {
		return nil
	}
	return t.deliver(next)
}

func (t *turn) executeToolRound(calls []ToolCall) ([]Input, error) {
	if err := t.ctx.Err(); err != nil {
		_ = t.tools.closeRemaining(err)
		return unexecutedToolInputs(calls, err), err
	}

	for _, call := range calls {
		if err := t.ctx.Err(); err != nil {
			_ = t.tools.closeRemaining(err)
			return unexecutedToolInputs(calls, err), err
		}
		if err := t.tools.beginExecution(call); err != nil {
			_ = t.tools.closeRemaining(err)
			return unexecutedToolInputs(calls, err), err
		}
	}

	if err := t.ctx.Err(); err != nil {
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
			toolCtx := context.WithValue(roundCtx, steeringSignalKey{}, steering)
			result, err := executeTool(toolCtx, t.engine.tools, call, t.tools.update(call))
			if updateErr := t.tools.updateError(call); updateErr != nil {
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

func failedToolResult(call ToolCall, result ToolResult, cause error) ToolResult {
	result.CallID = call.ID
	result.Tool = call.Name
	if result.Output == "" {
		result.Output = cause.Error()
	}
	result.IsError = true
	return result
}

func executeTool(ctx context.Context, tools Toolbox, call ToolCall, updates ToolUpdateSink) (ToolResult, error) {
	result, err := tools.Execute(ctx, call, updates)
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
