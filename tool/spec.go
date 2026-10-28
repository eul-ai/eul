package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/eul-ai/eul/agent"
)

type spec[A any] struct {
	definition agent.ToolDefinition
	present    func(A) agent.ToolPresentation
	run        func(context.Context, A, agent.ToolUpdateSink) (agent.ToolResult, error)
}

func (s spec[A]) Definition() agent.ToolDefinition {
	return s.definition
}

// Presentation decodes partial streaming arguments leniently: fields whose
// values do not fit A are left at their zero values.
func (s spec[A]) Presentation(rawArguments string) agent.ToolPresentation {
	var args A
	encoded, _ := json.Marshal(parseStreamingJSONObject(rawArguments))
	_ = json.Unmarshal(encoded, &args)

	return s.present(args)
}

func (s spec[A]) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[A](arguments)
	if err != nil {
		return errorResult(s.definition.Name, err), nil
	}

	result, err := s.run(ctx, args, updates)
	switch {
	case err == nil:
		return result, nil
	case ctx.Err() != nil:
		return result, ctx.Err()
	default:
		return errorResult(s.definition.Name, err), nil
	}
}

func decodeArguments[T any](arguments json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("tool: decode arguments: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("tool: arguments contain multiple JSON values")
		}
		return value, fmt.Errorf("tool: decode trailing arguments: %w", err)
	}

	return value, nil
}
