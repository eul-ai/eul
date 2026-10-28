package terminal

import (
	"strings"

	"github.com/eul-ai/eul/agent"
)

const maxSessionDescriptionBytes = 120

type Checkpoint struct {
	Blocks       []Block  `json:"blocks,omitempty"`
	Input        string   `json:"input,omitempty"`
	Cursor       int      `json:"cursor,omitempty"`
	QueuedInputs []string `json:"queued_inputs,omitempty"`
}

type Block struct {
	Kind       blockKind              `json:"kind"`
	Text       string                 `json:"text,omitempty"`
	Content    []agent.ContentPart    `json:"content,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Tool       agent.ToolPresentation `json:"tool"`
}

func (checkpoint Checkpoint) Description() string {
	for _, block := range checkpoint.Blocks {
		if block.Kind != blockUser {
			continue
		}
		prompt := strings.TrimSpace(block.Text)
		if prompt == "" && contentHasImage(block.Content) {
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

func checkpointModel(model *tuiModel, queued [][]agent.ContentPart) Checkpoint {
	blocks := make([]Block, len(model.blocks))
	for index, block := range model.blocks {
		blocks[index] = Block{
			Kind:       block.kind,
			Text:       block.text,
			Content:    block.content,
			ToolCallID: block.toolCallID,
			ToolName:   block.toolName,
			Tool:       block.tool,
		}
	}

	var queuedInputs []string
	for _, content := range queued {
		text := contentText(content)
		if strings.TrimSpace(text) != "" {
			queuedInputs = append(queuedInputs, text)
		}
	}

	return Checkpoint{
		Blocks:       blocks,
		Input:        model.inputText(),
		Cursor:       model.textCursor(),
		QueuedInputs: queuedInputs,
	}
}

func restoreModelCheckpoint(model *tuiModel, checkpoint Checkpoint) {
	model.blocks = make([]conversationBlock, len(checkpoint.Blocks))
	for index, block := range checkpoint.Blocks {
		if block.Kind == blockToolPending {
			block.Kind = blockToolError
			block.Tool.Outcome = "interrupted"
		}
		model.blocks[index] = conversationBlock{
			kind:       block.Kind,
			text:       block.Text,
			content:    block.Content,
			toolCallID: block.ToolCallID,
			toolName:   block.ToolName,
			tool:       block.Tool,
		}
	}
	model.conversationChanged()

	model.input = editorItemsFromText(checkpoint.Input)
	model.cursor = min(max(checkpoint.Cursor, 0), len(model.input))
	if len(checkpoint.QueuedInputs) > 0 {
		queued := strings.Join(checkpoint.QueuedInputs, "\n\n")
		if strings.TrimSpace(checkpoint.Input) != "" {
			queued += "\n\n" + checkpoint.Input
		}
		model.input = editorItemsFromText(queued)
		model.cursor = len(model.input)
	}
}
