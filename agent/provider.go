package agent

import (
	"context"
	"encoding/json"
)

// InputKind identifies new input supplied to a provider generation.
type InputKind string

const (
	InputUser       InputKind = "user"
	InputToolResult InputKind = "tool_result"
)

// Input is provider-neutral input added since the previous generation.
type Input struct {
	Kind    InputKind
	Text    string
	CallID  string
	Tool    string
	IsError bool
}

// ToolCall is a model request to execute a named tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Usage contains provider-reported token usage.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// Request contains the provider-neutral data needed for one generation.
type Request struct {
	Model        string
	Instructions string
	Inputs       []Input
	Tools        []ToolDefinition
	State        []byte
}

// Response is a completed provider generation.
type Response struct {
	Text      string
	ToolCalls []ToolCall
	State     []byte
	Usage     Usage
}

// TextSink receives assistant text as it becomes available. Streaming providers
// call it with ordered deltas.
type TextSink func(text string) error

// Provider generates assistant responses for the agent engine.
type Provider interface {
	Generate(ctx context.Context, request Request, onText TextSink) (Response, error)
}
