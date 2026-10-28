package responses

import (
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

type requestBuild struct {
	wire     createResponseRequest
	history  []json.RawMessage
	newItems []json.RawMessage
}

func (client *Client) buildGenerationRequest(request agent.Request) (requestBuild, error) {
	build, err := client.buildWireRequest(request)
	if err != nil {
		return requestBuild{}, fmt.Errorf("build request: %w", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return requestBuild{}, err
	}
	build.wire = configureGenerationRequest(configureCommonRequest(build.wire, options), options)
	return build, nil
}

func (client *Client) buildSummaryRequest(request agent.Request) (requestBuild, error) {
	build, err := client.buildWireRequest(request)
	if err != nil {
		return requestBuild{}, fmt.Errorf("build summary request: %w", err)
	}
	options, err := client.optionsFor(request)
	if err != nil {
		return requestBuild{}, err
	}
	build.wire = configureCommonRequest(build.wire, options)
	return build, nil
}

func (client *Client) buildWireRequest(request agent.Request) (requestBuild, error) {
	history, err := protocol.DecodeState(request.State)
	if err != nil {
		return requestBuild{}, err
	}

	newItems, err := client.encodeInputs(request.Inputs)
	if err != nil {
		return requestBuild{}, err
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

	return requestBuild{
		wire: createResponseRequest{
			Model:        request.Model,
			Instructions: request.Instructions,
			Input:        input,
			Tools:        tools,
			Store:        false,
			Stream:       false,
		},
		history:  history,
		newItems: newItems,
	}, nil
}

func configureCommonRequest(wireRequest createResponseRequest, options RequestOptions) createResponseRequest {
	wireRequest.SessionID = options.SessionID
	wireRequest.ServiceTier = options.ServiceTier
	wireRequest.Stream = true
	if options.Reasoning != nil {
		reasoning := *options.Reasoning
		wireRequest.Reasoning = &reasoning
	}
	wireRequest.Include = append([]string(nil), options.Include...)
	if options.TextVerbosity != "" {
		wireRequest.Text = &responseText{Verbosity: options.TextVerbosity}
	}
	return wireRequest
}

func configureGenerationRequest(wireRequest createResponseRequest, options RequestOptions) createResponseRequest {
	wireRequest.ToolChoice = options.ToolChoice
	wireRequest.ParallelToolCalls = options.ParallelToolCalls
	return wireRequest
}
