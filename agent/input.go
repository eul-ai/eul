package agent

import (
	"errors"
	"fmt"
	"strings"
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
	return Input{Kind: InputUser, Content: parts}
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
