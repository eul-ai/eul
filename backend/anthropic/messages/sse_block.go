package messages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type blockAccumulator struct {
	index        int
	kind         string
	text         strings.Builder
	thinking     strings.Builder
	signature    strings.Builder
	toolID       string
	toolName     string
	initialInput json.RawMessage
	toolInput    strings.Builder
	hasToolDelta bool
	redacted     string
	complete     bool
}

type blockUpdate struct {
	text      string
	reasoning string
	tool      bool
}

func newBlockAccumulator(index int, raw json.RawMessage) (*blockAccumulator, blockUpdate, error) {
	if err := validateRawObject(raw); err != nil {
		return nil, blockUpdate{}, fmt.Errorf("anthropic content block %d: %w", index, err)
	}

	var wire contentBlock
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, blockUpdate{}, fmt.Errorf("decode Anthropic content block %d: %w", index, err)
	}

	block := &blockAccumulator{
		index:        index,
		kind:         wire.Type,
		toolID:       wire.ID,
		toolName:     wire.Name,
		initialInput: append(json.RawMessage(nil), wire.Input...),
		redacted:     wire.Data,
	}
	block.text.WriteString(wire.Text)
	block.thinking.WriteString(wire.Thinking)
	block.signature.WriteString(wire.Signature)

	switch wire.Type {
	case "text":
		return block, blockUpdate{text: wire.Text}, nil
	case "thinking":
		return block, blockUpdate{reasoning: wire.Thinking}, nil
	case "tool_use":
		if wire.ID == "" || wire.Name == "" {
			return nil, blockUpdate{}, fmt.Errorf("anthropic tool block %d starts without an ID and name", index)
		}
		return block, blockUpdate{tool: true}, nil
	case "redacted_thinking":
		return block, blockUpdate{}, nil
	default:
		return nil, blockUpdate{}, fmt.Errorf("unsupported Anthropic content block type %q", wire.Type)
	}
}

func (block *blockAccumulator) apply(delta streamDelta) (blockUpdate, error) {
	if block.complete {
		return blockUpdate{}, fmt.Errorf("anthropic content block %d received a delta after stop", block.index)
	}

	switch delta.Type {
	case "text_delta":
		if block.kind != "text" {
			return blockUpdate{}, fmt.Errorf("anthropic content block %d received text for type %q", block.index, block.kind)
		}
		block.text.WriteString(delta.Text)
		return blockUpdate{text: delta.Text}, nil
	case "thinking_delta":
		if block.kind != "thinking" {
			return blockUpdate{}, fmt.Errorf("anthropic content block %d received thinking for type %q", block.index, block.kind)
		}
		block.thinking.WriteString(delta.Thinking)
		return blockUpdate{reasoning: delta.Thinking}, nil
	case "signature_delta":
		if block.kind != "thinking" {
			return blockUpdate{}, fmt.Errorf("anthropic content block %d received a signature for type %q", block.index, block.kind)
		}
		block.signature.WriteString(delta.Signature)
		return blockUpdate{}, nil
	case "input_json_delta":
		if block.kind != "tool_use" {
			return blockUpdate{}, fmt.Errorf("anthropic content block %d received tool input for type %q", block.index, block.kind)
		}
		block.hasToolDelta = true
		block.toolInput.WriteString(delta.PartialJSON)
		return blockUpdate{tool: true}, nil
	default:
		return blockUpdate{}, fmt.Errorf("unsupported Anthropic content block delta type %q", delta.Type)
	}
}

func (block *blockAccumulator) stop() (blockUpdate, error) {
	if block.complete {
		return blockUpdate{}, fmt.Errorf("anthropic content block %d stopped more than once", block.index)
	}
	block.complete = true
	if block.kind != "tool_use" {
		return blockUpdate{}, nil
	}

	if err := validateRawObject(json.RawMessage(block.toolArguments())); err != nil {
		return blockUpdate{}, fmt.Errorf("anthropic tool call %q input: %w", block.toolID, err)
	}
	return blockUpdate{tool: true}, nil
}

func (block *blockAccumulator) toolArguments() string {
	if block.hasToolDelta {
		arguments := block.toolInput.String()
		if arguments != "" {
			return arguments
		}
	}
	if len(block.initialInput) != 0 && string(block.initialInput) != "null" {
		return string(block.initialInput)
	}
	return "{}"
}

func (block *blockAccumulator) finalize(seenCalls map[string]struct{}) (contentBlock, *agent.ToolCall, string, error) {
	if !block.complete {
		return contentBlock{}, nil, "", fmt.Errorf("anthropic content block %d did not stop", block.index)
	}

	switch block.kind {
	case "text":
		text := block.text.String()
		return contentBlock{Type: "text", Text: text}, nil, text, nil
	case "thinking":
		return contentBlock{
			Type:      "thinking",
			Thinking:  block.thinking.String(),
			Signature: block.signature.String(),
		}, nil, "", nil
	case "redacted_thinking":
		return contentBlock{Type: "redacted_thinking", Data: block.redacted}, nil, "", nil
	case "tool_use":
		arguments := json.RawMessage(block.toolArguments())
		if _, exists := seenCalls[block.toolID]; exists {
			return contentBlock{}, nil, "", fmt.Errorf("anthropic message has duplicate tool call ID %q", block.toolID)
		}
		seenCalls[block.toolID] = struct{}{}

		call := &agent.ToolCall{ID: block.toolID, Name: block.toolName, Arguments: arguments}
		wire := contentBlock{Type: "tool_use", ID: block.toolID, Name: block.toolName, Input: arguments}
		return wire, call, "", nil
	default:
		return contentBlock{}, nil, "", fmt.Errorf("unsupported Anthropic content block type %q", block.kind)
	}
}
