package anthropic

import (
	"encoding/json"
	"io"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

type RequestOptions struct {
	MaxTokens  int
	Thinking   *Thinking
	ToolChoice *ToolChoice
}

type Codec struct {
	Options func(agent.Request) (RequestOptions, error)
}

func (codec Codec) Build(request agent.Request, history []json.RawMessage) (protocol.Wire, []json.RawMessage, error) {
	newMessages, err := encodeInputs(request.Inputs)
	if err != nil {
		return nil, nil, err
	}
	options, err := codec.Options(request)
	if err != nil {
		return nil, nil, err
	}

	messages := make([]json.RawMessage, 0, len(history)+len(newMessages))
	messages = append(messages, history...)
	messages = append(messages, newMessages...)

	tools := make([]toolDefinition, len(request.Tools))
	for index, definition := range request.Tools {
		tools[index] = toolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: definition.Parameters,
		}
	}

	wire := createRequest{
		Model:     request.Model,
		Messages:  messages,
		Tools:     tools,
		MaxTokens: options.MaxTokens,
		Stream:    true,
		Thinking:  options.Thinking,
	}
	if request.Instructions != "" {
		wire.System = []systemBlock{{Type: "text", Text: request.Instructions}}
	}
	if len(tools) != 0 {
		wire.ToolChoice = options.ToolChoice
	}

	wire, err = withPromptCacheControl(wire)
	if err != nil {
		return nil, nil, err
	}
	return wire, newMessages, nil
}

func (createRequest) Read(body io.Reader, observer agent.StreamObserver) (protocol.Result, error) {
	return readMessagesSSE(body, observer)
}

func (createRequest) SummaryItems(summary string, continueTask bool) []json.RawMessage {
	question, _ := marshalWireMessage("user", []contentBlock{{Type: "text", Text: protocol.SummaryQuestion}})
	answer, _ := marshalWireMessage("assistant", []contentBlock{{Type: "text", Text: summary}})
	items := []json.RawMessage{question, answer}
	if !continueTask {
		return items
	}

	continuation, _ := marshalWireMessage("user", []contentBlock{{Type: "text", Text: protocol.Continuation}})
	return append(items, continuation)
}
