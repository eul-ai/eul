package responses

import (
	"encoding/json"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

type RequestOptions struct {
	SessionID         string
	Reasoning         *Reasoning
	ServiceTier       string
	TextVerbosity     string
	Include           []string
	ToolChoice        string
	ParallelToolCalls bool
}

type Codec struct {
	Options             func(agent.Request) (RequestOptions, error)
	InboxAsAgentMessage bool
}

func (codec Codec) Build(request agent.Request, history []json.RawMessage) (protocol.Wire, []json.RawMessage, error) {
	return codec.build(request, history)
}

func (codec Codec) BuildCompaction(request agent.Request, history []json.RawMessage) (protocol.Wire, []json.RawMessage, error) {
	wire, newItems, err := codec.build(request, history)
	if err != nil {
		return nil, nil, err
	}
	wire.Input = append(wire.Input, json.RawMessage(`{"type":"compaction_trigger"}`))
	return wire, newItems, nil
}

func (codec Codec) build(request agent.Request, history []json.RawMessage) (createResponseRequest, []json.RawMessage, error) {
	newItems, err := codec.encodeInputs(request.Inputs)
	if err != nil {
		return createResponseRequest{}, nil, err
	}
	options, err := codec.Options(request)
	if err != nil {
		return createResponseRequest{}, nil, err
	}

	input := make([]json.RawMessage, 0, len(history)+len(newItems))
	input = append(input, history...)
	input = append(input, newItems...)

	tools := make([]functionTool, len(request.Tools))
	for i, definition := range request.Tools {
		tools[i] = functionTool{
			Type:        "function",
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
			Strict:      true,
		}
	}

	wire := createResponseRequest{
		SessionID:    options.SessionID,
		Model:        request.Model,
		ServiceTier:  options.ServiceTier,
		Instructions: request.Instructions,
		Input:        input,
		Tools:        tools,
		Stream:       true,
		Include:      options.Include,
		Reasoning:    options.Reasoning,
	}
	if options.TextVerbosity != "" {
		wire.Text = &responseText{Verbosity: options.TextVerbosity}
	}
	if len(tools) != 0 {
		wire.ToolChoice = options.ToolChoice
		wire.ParallelToolCalls = options.ParallelToolCalls
	}
	return wire, newItems, nil
}
