package agent

import "strings"

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
