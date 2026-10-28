package chatcompletions

import (
	"encoding/json"
	"io"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

type RequestOptions struct {
	MaxTokens                 int
	ReasoningEffort           string
	ToolChoice                string
	ParallelToolCalls         bool
	SerializeReasoningContent bool
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

	messages := make([]json.RawMessage, 0, len(history)+len(newMessages)+1)
	if request.Instructions != "" {
		system, _ := json.Marshal(message{Role: "system", Content: request.Instructions})
		messages = append(messages, system)
	}
	messages = append(messages, history...)
	messages = append(messages, newMessages...)

	tools := make([]functionTool, len(request.Tools))
	for index, definition := range request.Tools {
		tools[index] = functionTool{
			Type: "function",
			Function: toolDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
				Strict:      true,
			},
		}
	}

	wire := createRequest{
		Model:                     request.Model,
		Messages:                  messages,
		Tools:                     tools,
		Stream:                    true,
		StreamOptions:             &streamOptions{IncludeUsage: true},
		MaxTokens:                 options.MaxTokens,
		ReasoningEffort:           options.ReasoningEffort,
		serializeReasoningContent: options.SerializeReasoningContent,
	}
	if len(tools) != 0 {
		wire.ToolChoice = options.ToolChoice
		if options.ParallelToolCalls {
			enabled := true
			wire.ParallelToolCalls = &enabled
		}
	}
	return wire, newMessages, nil
}

func (request createRequest) Read(body io.Reader, observer agent.StreamObserver) (protocol.Result, error) {
	return readCompletionSSE(body, observer, request.serializeReasoningContent)
}

func (request createRequest) SummaryItems(summary string, continueTask bool) []json.RawMessage {
	question, _ := json.Marshal(message{Role: "user", Content: protocol.SummaryQuestion})
	answer, _ := json.Marshal(assistantMessage{
		Role:             "assistant",
		Content:          summary,
		ReasoningContent: reasoningContent("", request.serializeReasoningContent),
	})
	items := []json.RawMessage{question, answer}
	if !continueTask {
		return items
	}

	continuation, _ := json.Marshal(message{Role: "user", Content: protocol.Continuation})
	return append(items, continuation)
}
