package agent

import (
	"context"
	"encoding/json"
	"time"
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
	Complete     bool
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type ProviderUsage struct {
	Windows []UsageWindow
}

type UsageWindow struct {
	Duration    time.Duration
	UsedPercent int
	ResetsAt    time.Time
}

type Request struct {
	Model         string
	ThinkingLevel ThinkingLevel
	FastMode      bool
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

type ToolCallSink func(ToolCallSnapshot) error

// StreamObserver callbacks may be called concurrently while Generate is running.
// Returning an error stops delivery and terminates generation.
type StreamObserver struct {
	Text      TextSink
	Reasoning TextSink
	ToolCall  ToolCallSink
}

type Provider interface {
	Generate(context.Context, Request, StreamObserver) (Response, error)
}

// GenerationRetryPolicy decides whether a failed generation attempt should be retried.
// failedAttempts includes the attempt that returned err.
type GenerationRetryPolicy interface {
	RetryGeneration(err error, failedAttempts int) (time.Duration, bool)
}

type UsageProvider interface {
	Usage(context.Context) (ProviderUsage, error)
}

type ModelMetadata struct {
	ContextWindow  int64
	ThinkingLevels []ThinkingLevel
	FastMode       bool
}

func (metadata ModelMetadata) ClampThinkingLevel(level ThinkingLevel) ThinkingLevel {
	return ClampThinkingLevel(level, metadata.ThinkingLevels)
}

type ModelMetadataProvider interface {
	ModelMetadata(model string) ModelMetadata
}

type Compactor interface {
	ShouldCompact(Request, Usage) bool
	Compact(context.Context, Request) (CompactResponse, error)
}

// CompactionErrorPolicy decides whether a failed generation should be retried after compacting its request.
type CompactionErrorPolicy interface {
	ShouldCompactAfterError(Request, error) bool
}
