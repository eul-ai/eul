package responses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
)

const (
	continuationStateVersion       = 1
	continuationStateEnvelopeBytes = len(`{"version":1,"items":[]}`)
)

type createResponseRequest struct {
	SessionID         string            `json:"session_id,omitempty"`
	Model             string            `json:"model"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	Instructions      string            `json:"instructions"`
	Input             []json.RawMessage `json:"input"`
	Tools             []functionTool    `json:"tools"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include,omitempty"`
	Text              *responseText     `json:"text,omitempty"`
	Reasoning         *Reasoning        `json:"reasoning,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
}

type responseText struct {
	Verbosity string `json:"verbosity"`
}

type Reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type functionTool struct {
	Type        string           `json:"type"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Parameters  agent.JSONSchema `json:"parameters"`
	Strict      bool             `json:"strict"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type inputContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type functionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type continuationState struct {
	Version int               `json:"version"`
	Items   []json.RawMessage `json:"items"`
}

type requestBuild struct {
	wire     createResponseRequest
	history  []json.RawMessage
	newItems []json.RawMessage
}

func (client *Client) buildGenerationRequest(request agent.Request) (requestBuild, error) {
	build, err := buildRequest(request, client.maxStateBytes, client.generationStateBytes())
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
	build, err := buildRequestUnchecked(request, client.maxStateBytes)
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

func buildRequest(request agent.Request, maxStateBytes, generationStateBytes int) (requestBuild, error) {
	build, err := buildRequestUnchecked(request, maxStateBytes)
	if err != nil {
		return requestBuild{}, err
	}
	if _, err := encodeState(build.history, build.newItems, nil, generationStateBytes); err != nil {
		return requestBuild{}, err
	}
	return build, nil
}

func buildRequestUnchecked(request agent.Request, maxStateBytes int) (requestBuild, error) {
	history, err := decodeState(request.State, maxStateBytes)
	if err != nil {
		return requestBuild{}, err
	}

	newItems, err := encodeInputs(request.Inputs)
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

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("input %d: %w", index, err)
		}

		var value any
		switch input.Kind {
		case agent.InputUser:
			value = inputMessage{Role: "user", Content: encodeUserContent(input.Content)}
		case agent.InputInbox:
			value = inputMessage{Role: "user", Content: input.Text}
		case agent.InputToolResult:
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = functionCallOutput{Type: "function_call_output", CallID: input.CallID, Output: output}
		}
		items[index], _ = json.Marshal(value)
	}
	return items, nil
}

func encodeUserContent(content []agent.ContentPart) any {
	parts := make([]inputContentPart, 0, len(content))
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			parts = append(parts, inputContentPart{Type: "input_text", Text: part.Text})
		case agent.ContentPartImage:
			image := part.Image
			if image == nil {
				continue
			}
			parts = append(parts, inputContentPart{
				Type:     "input_image",
				ImageURL: "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
			})
		}
	}
	return parts
}

func decodeState(encoded []byte, maxStateBytes int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}

	var state continuationState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if state.Version != continuationStateVersion {
		return nil, fmt.Errorf("unsupported continuation state version %d", state.Version)
	}
	for index, item := range state.Items {
		if err := validateRawObject(item); err != nil {
			return nil, fmt.Errorf("continuation state item %d: %w", index, err)
		}
	}

	return state.Items, nil
}

func encodeState(history, newInputs, output []json.RawMessage, maxStateBytes int) ([]byte, error) {
	items := continuationStateItems(history, newInputs, output)

	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Items: items})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}

	return encoded, nil
}

func continuationStateItems(groups ...[]json.RawMessage) []json.RawMessage {
	return continuation.RawMessages(groups...)
}

func validateRawObject(value json.RawMessage) error {
	return continuation.ValidateRawObject(value)
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
