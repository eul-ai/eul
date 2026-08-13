package terminal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const (
	terminalCheckpointVersion  = 1
	maxSessionDescriptionBytes = 120
)

type Checkpoint struct {
	data terminalCheckpointData
}

type terminalCheckpointData struct {
	Version       int               `json:"version"`
	Blocks        []checkpointBlock `json:"blocks,omitempty"`
	Input         string            `json:"input,omitempty"`
	Cursor        int               `json:"cursor,omitempty"`
	History       []string          `json:"history,omitempty"`
	ContextTokens int64             `json:"context_tokens,omitempty"`
	QueuedInputs  []string          `json:"queued_inputs,omitempty"`
}

type checkpointBlock struct {
	Kind        blockKind               `json:"kind"`
	Text        string                  `json:"text,omitempty"`
	Content     []checkpointContentPart `json:"content,omitempty"`
	ToolCallID  string                  `json:"tool_call_id,omitempty"`
	Tool        agent.ToolPresentation  `json:"tool,omitempty"`
	ToolOutcome string                  `json:"tool_outcome,omitempty"`
}

type checkpointContentPart struct {
	Kind agent.ContentPartKind `json:"kind"`
	Text string                `json:"text,omitempty"`
}

func EmptyCheckpoint() Checkpoint {
	return Checkpoint{data: terminalCheckpointData{Version: terminalCheckpointVersion}}
}

func (checkpoint Checkpoint) Initialized() bool {
	return checkpoint.data.Version == terminalCheckpointVersion
}

func (checkpoint Checkpoint) MarshalJSON() ([]byte, error) {
	data := cloneTerminalCheckpointData(checkpoint.data)
	if err := validateTerminalCheckpointData(data); err != nil {
		return nil, err
	}
	return json.Marshal(data)
}

func (checkpoint *Checkpoint) UnmarshalJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	var data terminalCheckpointData
	if err := decoder.Decode(&data); err != nil {
		return fmt.Errorf("terminal: decode checkpoint: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("terminal: decode checkpoint: multiple JSON values")
		}
		return fmt.Errorf("terminal: decode checkpoint: %w", err)
	}
	if err := validateTerminalCheckpointData(data); err != nil {
		return err
	}

	checkpoint.data = cloneTerminalCheckpointData(data)
	return nil
}

func (checkpoint Checkpoint) Description() string {
	for _, block := range checkpoint.data.Blocks {
		if block.Kind != blockUser {
			continue
		}
		prompt := strings.TrimSpace(block.Text)
		if prompt == "" {
			for _, part := range block.Content {
				if part.Kind == agent.ContentPartText {
					prompt += part.Text
				}
			}
			prompt = strings.TrimSpace(prompt)
		}
		if prompt == "" && checkpointContentHasImage(block.Content) {
			return "Image attachment"
		}
		if prompt == "" {
			continue
		}
		line, _, _ := strings.Cut(prompt, "\n")
		return singleLine(strings.TrimSpace(line), maxSessionDescriptionBytes)
	}
	return ""
}

func checkpointContentHasImage(content []checkpointContentPart) bool {
	for _, part := range content {
		if part.Kind == agent.ContentPartImage {
			return true
		}
	}
	return false
}

func validateTerminalCheckpointData(data terminalCheckpointData) error {
	if data.Version != terminalCheckpointVersion {
		return fmt.Errorf("terminal: unsupported checkpoint version %d", data.Version)
	}
	if !utf8.ValidString(data.Input) || len(data.Input) > maxInputBytes {
		return errors.New("terminal: checkpoint input is invalid")
	}
	inputRunes := []rune(data.Input)
	if data.Cursor < 0 || data.Cursor > len(inputRunes) {
		return errors.New("terminal: checkpoint cursor is out of range")
	}
	if data.ContextTokens < 0 {
		return errors.New("terminal: checkpoint context usage is negative")
	}
	for index, block := range data.Blocks {
		if block.Kind < blockUser || block.Kind > blockInfo {
			return fmt.Errorf("terminal: checkpoint block %d has unknown kind %d", index, block.Kind)
		}
		for partIndex, part := range block.Content {
			switch part.Kind {
			case agent.ContentPartText, agent.ContentPartImage:
			default:
				return fmt.Errorf("terminal: checkpoint block %d content part %d has unknown kind %q", index, partIndex, part.Kind)
			}
		}
	}
	for index, prompt := range data.History {
		if !utf8.ValidString(prompt) || len(prompt) > maxInputBytes {
			return fmt.Errorf("terminal: checkpoint history entry %d is invalid", index)
		}
	}
	restoredInputBytes := len(data.Input)
	for index, prompt := range data.QueuedInputs {
		if !utf8.ValidString(prompt) || len(prompt) > maxInputBytes {
			return fmt.Errorf("terminal: checkpoint queued input %d is invalid", index)
		}
		restoredInputBytes += len(prompt)
		if index > 0 || data.Input != "" {
			restoredInputBytes += 2
		}
	}
	if restoredInputBytes > maxInputBytes {
		return errors.New("terminal: checkpoint restored input is too long")
	}
	return nil
}

func cloneTerminalCheckpointData(data terminalCheckpointData) terminalCheckpointData {
	blocks := data.Blocks
	data.Blocks = make([]checkpointBlock, len(blocks))
	for index, block := range blocks {
		block.Content = append([]checkpointContentPart(nil), block.Content...)
		block.Tool = block.Tool.Clone()
		data.Blocks[index] = block
	}
	data.History = append([]string(nil), data.History...)
	data.QueuedInputs = append([]string(nil), data.QueuedInputs...)
	return data
}

func checkpointContent(content []agent.ContentPart) []checkpointContentPart {
	parts := make([]checkpointContentPart, 0, len(content))
	for _, part := range content {
		parts = append(parts, checkpointContentPart{Kind: part.Kind, Text: part.Text})
	}
	return parts
}

func restoreCheckpointContent(content []checkpointContentPart) []agent.ContentPart {
	parts := make([]agent.ContentPart, 0, len(content))
	for _, part := range content {
		parts = append(parts, agent.ContentPart{Kind: part.Kind, Text: part.Text})
	}
	return parts
}

func checkpointModel(model *tuiModel, queued []string) Checkpoint {
	blocks := make([]checkpointBlock, len(model.blocks))
	for index, block := range model.blocks {
		blocks[index] = checkpointBlock{
			Kind:        block.kind,
			Text:        block.text,
			Content:     checkpointContent(block.content),
			ToolCallID:  block.toolCallID,
			Tool:        block.tool.Clone(),
			ToolOutcome: block.toolOutcome,
		}
	}

	return Checkpoint{data: terminalCheckpointData{
		Version:       terminalCheckpointVersion,
		Blocks:        blocks,
		Input:         model.inputText(),
		Cursor:        model.textCursor(),
		History:       append([]string(nil), model.history...),
		ContextTokens: model.contextTokens,
		QueuedInputs:  append([]string(nil), queued...),
	}}
}

func restoreModelCheckpoint(model *tuiModel, checkpoint Checkpoint) {
	data := cloneTerminalCheckpointData(checkpoint.data)
	model.blocks = make([]conversationBlock, len(data.Blocks))
	for index, block := range data.Blocks {
		kind := block.Kind
		outcome := block.ToolOutcome
		if kind == blockToolPending {
			kind = blockToolError
			outcome = "interrupted"
		}
		model.blocks[index] = conversationBlock{
			kind:        kind,
			text:        sanitizeAssistantText(block.Text),
			content:     sanitizeContent(restoreCheckpointContent(block.Content)),
			toolCallID:  block.ToolCallID,
			tool:        sanitizeToolPresentation(agent.ToolCall{ID: block.ToolCallID}, block.Tool),
			toolOutcome: sanitizeAssistantText(outcome),
		}
	}
	model.history = data.History
	model.contextTokens = data.ContextTokens
	model.input = editorItemsFromText(data.Input)
	model.cursor = data.Cursor
	if len(data.QueuedInputs) > 0 {
		queued := strings.Join(data.QueuedInputs, "\n\n")
		if strings.TrimSpace(data.Input) != "" {
			queued += "\n\n" + data.Input
		}
		model.input = editorItemsFromText(queued)
		model.cursor = len(model.input)
	}
	model.conversationVersion++
	model.running = false
	model.interrupted = false
	model.activity = activity{kind: activityReady}
	model.streamOpen = false
	model.historyIndex = -1
	model.historyDraft = nil
	model.historyDraftCursor = 0
}
