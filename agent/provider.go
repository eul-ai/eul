package agent

import (
	"context"
	"encoding/json"
)

type InputKind string

const (
	InputUser       InputKind = "user"
	InputToolResult InputKind = "tool_result"
)

type Input struct {
	Kind    InputKind
	Text    string
	CallID  string
	Tool    string
	IsError bool
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ToolCallSnapshot struct {
	ID           string
	Name         string
	RawArguments string
	Arguments    map[string]any
	Complete     bool
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type Request struct {
	Model         string
	ThinkingLevel ThinkingLevel
	Instructions  string
	Inputs        []Input
	Tools         []ToolDefinition
	State         []byte
}

type Response struct {
	Text      string
	ToolCalls []ToolCall
	State     []byte
	Usage     Usage
}

type CompactResponse struct {
	State []byte
	Usage Usage
}

type TextSink func(text string) error

// ToolCallSink may be called concurrently while Generate is running.
type ToolCallSink func(ToolCallSnapshot) error

type Provider interface {
	Generate(ctx context.Context, request Request, onText, onReasoning TextSink, onToolCall ToolCallSink) (Response, error)
}

type Compactor interface {
	ShouldCompact(Request, Usage) bool
	Compact(context.Context, Request) (CompactResponse, error)
}
