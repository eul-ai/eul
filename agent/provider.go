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

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type Request struct {
	Model        string
	Instructions string
	Inputs       []Input
	Tools        []ToolDefinition
	State        []byte
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

type Provider interface {
	Generate(ctx context.Context, request Request, onText, onReasoning TextSink) (Response, error)
}

type Compactor interface {
	ShouldCompact(Request, Usage) bool
	Compact(context.Context, Request) (CompactResponse, error)
}
