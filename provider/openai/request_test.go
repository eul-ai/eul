package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"yaah/agent"
)

func TestBuildCreateRequestValidatesInputsAndState(t *testing.T) {
	pendingState := mustState(t,
		json.RawMessage(`{"role":"user","content":"run"}`),
		json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}`),
	)
	resolvedState := mustState(t,
		json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
	)

	tests := []struct {
		name    string
		request agent.Request
		want    string
	}{
		{name: "missing model", request: agent.Request{}, want: "model is required"},
		{name: "unsupported input", request: agent.Request{Model: "model", Inputs: []agent.Input{{Kind: "other"}}}, want: "unsupported kind"},
		{name: "tool result without state", request: agent.Request{Model: "model", Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read"}}}, want: "unknown call ID"},
		{name: "unknown call", request: agent.Request{Model: "model", State: pendingState, Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "other", Tool: "read"}}}, want: "unknown call ID"},
		{name: "wrong tool", request: agent.Request{Model: "model", State: pendingState, Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "bash"}}}, want: "want \"read\""},
		{name: "missing tool name", request: agent.Request{Model: "model", State: pendingState, Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1"}}}, want: "no tool name"},
		{name: "missing one output", request: agent.Request{Model: "model", State: mustState(t,
			json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"read"}`),
			json.RawMessage(`{"type":"function_call","call_id":"call_2","name":"bash"}`),
		), Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read"}}}, want: "do not resolve every"},
		{name: "user while pending", request: agent.Request{Model: "model", State: pendingState, Inputs: []agent.Input{{Kind: agent.InputUser, Text: "next"}}}, want: "unresolved function calls"},
		{name: "already resolved", request: agent.Request{Model: "model", State: resolvedState, Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read"}}}, want: "unknown call ID"},
		{name: "duplicate call in state", request: agent.Request{Model: "model", State: mustState(t,
			json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"read"}`),
			json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"read"}`),
		), Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read"}}}, want: "duplicate continuation"},
		{name: "malformed state", request: agent.Request{Model: "model", State: []byte(`{`)}, want: "decode continuation state"},
		{name: "wrong state version", request: agent.Request{Model: "model", State: []byte(`{"version":2,"items":[]}`)}, want: "unsupported continuation state version"},
		{name: "unknown state field", request: agent.Request{Model: "model", State: []byte(`{"version":1,"items":[],"extra":true}`)}, want: "unknown field"},
		{name: "null state items", request: agent.Request{Model: "model", State: []byte(`{"version":1,"items":null}`)}, want: "items must be an array"},
		{name: "non-object state item", request: agent.Request{Model: "model", State: []byte(`{"version":1,"items":[3]}`)}, want: "must be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := buildCreateRequest(test.request, defaultMaxStateBytes)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildCreateRequest() error = %v, want containing %q", err, test.want)
			}
		})
	}

	_, _, err := buildCreateRequest(agent.Request{Model: "model", State: []byte(strings.Repeat("x", 11))}, 10)
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 bytes") {
		t.Fatalf("oversized state error = %v", err)
	}
}

func TestBuildCreateRequestValidatesStrictTools(t *testing.T) {
	additionalProperties := false
	allowAdditionalProperties := true
	valid := strictTestTool("read")
	tests := []struct {
		name string
		tool agent.ToolDefinition
		want string
	}{
		{name: "missing name", tool: agent.ToolDefinition{Parameters: valid.Parameters}, want: "has no name"},
		{name: "invalid name", tool: agent.ToolDefinition{Name: "bad name", Parameters: valid.Parameters}, want: "letters, digits"},
		{name: "non-object root", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "string"}}, want: "type object"},
		{name: "additional properties omitted", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "object", Properties: map[string]agent.JSONSchema{"path": {Type: "string"}}, Required: []string{"path"}}}, want: "additionalProperties"},
		{name: "additional properties true", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "object", Properties: map[string]agent.JSONSchema{"path": {Type: "string"}}, Required: []string{"path"}, AdditionalProperties: &allowAdditionalProperties}}, want: "additionalProperties"},
		{name: "missing required", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "object", Properties: map[string]agent.JSONSchema{"path": {Type: "string"}}, AdditionalProperties: &additionalProperties}}, want: "must be required"},
		{name: "unknown required", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "object", Properties: map[string]agent.JSONSchema{"path": {Type: "string"}}, Required: []string{"other"}, AdditionalProperties: &additionalProperties}}, want: "unknown property"},
		{name: "duplicate required", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "object", Properties: map[string]agent.JSONSchema{"path": {Type: "string"}}, Required: []string{"path", "path"}, AdditionalProperties: &additionalProperties}}, want: "duplicate required"},
		{name: "invalid nested object", tool: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Type: "object", Properties: map[string]agent.JSONSchema{"options": {Type: "object", Properties: map[string]agent.JSONSchema{"limit": {Type: "integer"}}, Required: []string{"limit"}}}, Required: []string{"options"}, AdditionalProperties: &additionalProperties}}, want: "parameters.options must set"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := buildCreateRequest(agent.Request{Model: "model", Tools: []agent.ToolDefinition{test.tool}}, defaultMaxStateBytes)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildCreateRequest() error = %v, want containing %q", err, test.want)
			}
		})
	}

	_, _, err := buildCreateRequest(agent.Request{Model: "model", Tools: []agent.ToolDefinition{valid, valid}}, defaultMaxStateBytes)
	if err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Fatalf("duplicate tool error = %v", err)
	}

	recursive := agent.JSONSchema{Type: "array"}
	recursive.Items = &recursive
	recursiveTool := agent.ToolDefinition{
		Name: "recursive",
		Parameters: agent.JSONSchema{
			Type:                 "object",
			Properties:           map[string]agent.JSONSchema{"items": recursive},
			Required:             []string{"items"},
			AdditionalProperties: &additionalProperties,
		},
	}
	_, _, err = buildCreateRequest(agent.Request{Model: "model", Tools: []agent.ToolDefinition{recursiveTool}}, defaultMaxStateBytes)
	if err == nil || !strings.Contains(err.Error(), "maximum schema depth") {
		t.Fatalf("recursive schema error = %v", err)
	}
}

func TestRequestPayloadPreflightUpperBoundsEncodedRequest(t *testing.T) {
	additionalProperties := false
	requests := []agent.Request{
		baseRequest(),
		{
			Model:        "model<&>",
			Instructions: "quotes \" slashes \\ controls\n and separators \u2028",
			Inputs: []agent.Input{
				{Kind: agent.InputUser, Text: strings.Repeat("<&\n", 50)},
				{Kind: agent.InputUser, Text: "second"},
			},
			Tools: []agent.ToolDefinition{{
				Name:        "nested",
				Description: "description <&>",
				Parameters: agent.JSONSchema{
					Type: "object",
					Properties: map[string]agent.JSONSchema{
						"options": {
							Type:                 "object",
							Properties:           map[string]agent.JSONSchema{"limit": {AnyOf: []agent.JSONSchema{{Type: "integer"}, {Type: "null"}}}},
							Required:             []string{"limit"},
							AdditionalProperties: &additionalProperties,
						},
					},
					Required:             []string{"options"},
					AdditionalProperties: &additionalProperties,
				},
			}},
		},
		{
			Model: "model",
			State: mustState(t,
				json.RawMessage(`{"role":"user","content":"old <&>"}`),
				json.RawMessage(`{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}`),
			),
			Inputs: []agent.Input{{Kind: agent.InputUser, Text: "new"}},
		},
	}
	for i, request := range requests {
		wire, _, err := buildCreateRequest(request, defaultMaxStateBytes)
		if err != nil {
			t.Fatalf("request %d build error = %v", i, err)
		}
		encoded, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if !requestPayloadExceeds(request, int64(len(encoded))) {
			t.Fatalf("request %d preflight estimate is below encoded size %d", i, len(encoded))
		}
	}
}

func TestEncodeStateIsBoundedAndDefensive(t *testing.T) {
	history := []json.RawMessage{json.RawMessage(`{"role":"user","content":"hello"}`)}
	output := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}
	state, err := encodeState(history, nil, output, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	history[0][0] = '['
	output[0][0] = '['
	items, err := decodeState(state, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || string(items[0]) != `{"role":"user","content":"hello"}` || string(items[1]) != `{"type":"reasoning","encrypted_content":"opaque"}` {
		t.Fatalf("decoded state = %s", state)
	}
	if _, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"large":"` + strings.Repeat("x", 100) + `"}`)}, 50); err == nil || !strings.Contains(err.Error(), "exceeds 50 bytes") {
		t.Fatalf("oversized encoded state error = %v", err)
	}
}

func mustState(t *testing.T, items ...json.RawMessage) []byte {
	t.Helper()
	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
