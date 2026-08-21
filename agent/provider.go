package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type InputKind string

const (
	InputUser       InputKind = "user"
	InputToolResult InputKind = "tool_result"
	InputInbox      InputKind = "inbox"
)

type Image struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

type ContentPartKind string

const (
	ContentPartText  ContentPartKind = "text"
	ContentPartImage ContentPartKind = "image"
)

type ContentPart struct {
	Kind  ContentPartKind `json:"kind"`
	Text  string          `json:"text,omitempty"`
	Image *Image          `json:"image,omitempty"`
}

func cloneContentParts(parts []ContentPart) []ContentPart {
	if len(parts) == 0 {
		return nil
	}

	cloned := make([]ContentPart, len(parts))
	for index, part := range parts {
		cloned[index] = part
		if part.Image != nil {
			image := *part.Image
			image.Data = append([]byte(nil), image.Data...)
			cloned[index].Image = &image
		}
	}
	return cloned
}

func cloneInputs(inputs []Input) []Input {
	if len(inputs) == 0 {
		return nil
	}

	cloned := append([]Input(nil), inputs...)
	for index := range cloned {
		cloned[index].Content = cloneContentParts(cloned[index].Content)
	}
	return cloned
}

type Input struct {
	Kind    InputKind     `json:"kind"`
	Text    string        `json:"text"`
	Content []ContentPart `json:"content,omitempty"`
	CallID  string        `json:"call_id"`
	Tool    string        `json:"tool"`
	IsError bool          `json:"is_error"`
}

func (input Input) Validate() error {
	switch input.Kind {
	case InputUser:
		if input.Text != "" || input.CallID != "" || input.Tool != "" || input.IsError {
			return errors.New("user input has invalid metadata")
		}
		if len(input.Content) == 0 {
			return errors.New("user input has no content")
		}
		for index, part := range input.Content {
			if err := part.validate(); err != nil {
				return fmt.Errorf("user content part %d: %w", index, err)
			}
		}
	case InputToolResult:
		if input.CallID == "" || input.Tool == "" {
			return errors.New("tool result has incomplete metadata")
		}
		if len(input.Content) > 0 {
			return errors.New("tool result has user content")
		}
	case InputInbox:
		if input.Text == "" || len(input.Content) > 0 || input.CallID != "" || input.Tool != "" || input.IsError {
			return errors.New("inbox input has invalid metadata")
		}
	default:
		return fmt.Errorf("unknown input kind %q", input.Kind)
	}
	return nil
}

func NewUserInput(parts ...ContentPart) Input {
	return Input{Kind: InputUser, Content: cloneContentParts(parts)}
}

func NewTextInput(text string) Input {
	return NewUserInput(ContentPart{Kind: ContentPartText, Text: text})
}

func NewInboxInput(text string) Input {
	return Input{Kind: InputInbox, Text: text}
}

func NewToolResultInput(result ToolResult) Input {
	return Input{
		Kind:    InputToolResult,
		Text:    result.Output,
		CallID:  result.CallID,
		Tool:    result.Tool,
		IsError: result.IsError,
	}
}

func (input Input) PlainText() string {
	if input.Kind != InputUser {
		return input.Text
	}

	var text strings.Builder
	for _, part := range input.Content {
		if part.Kind == ContentPartText {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func (part ContentPart) validate() error {
	switch part.Kind {
	case ContentPartText:
		if part.Image != nil {
			return errors.New("text content has an image")
		}
	case ContentPartImage:
		if part.Image == nil {
			return errors.New("image content is missing an image")
		}
		if part.Text != "" {
			return errors.New("image content has text")
		}
	default:
		return fmt.Errorf("unknown content kind %q", part.Kind)
	}
	return nil
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
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type Request struct {
	SessionID     string
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

type Compactor interface {
	ShouldCompact(Request, Usage) bool
	Compact(context.Context, Request) (CompactResponse, error)
}

// CompactionErrorPolicy decides whether a failed generation should be retried after compacting its request.
type CompactionErrorPolicy interface {
	ShouldCompactAfterError(Request, error) bool
}
