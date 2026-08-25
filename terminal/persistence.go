package terminal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const checkpointStateVersion = 1

type Transcript struct {
	blocks []checkpointBlock
}

type CheckpointState struct {
	data checkpointStateData
}

type TranscriptDelta struct {
	replaceFrom int
	blocks      []checkpointBlock
}

type checkpointStateData struct {
	Version       int      `json:"version"`
	Input         string   `json:"input,omitempty"`
	Cursor        int      `json:"cursor,omitempty"`
	History       []string `json:"history,omitempty"`
	ContextTokens int64    `json:"context_tokens,omitempty"`
	QueuedInputs  []string `json:"queued_inputs,omitempty"`
}

type transcriptDeltaData struct {
	ReplaceFrom int               `json:"replace_from"`
	Blocks      []checkpointBlock `json:"blocks,omitempty"`
}

func EmptyTranscript() Transcript {
	return Transcript{}
}

func SplitCheckpoint(checkpoint Checkpoint) (Transcript, CheckpointState, error) {
	if err := validateTerminalCheckpointData(checkpoint.data); err != nil {
		return Transcript{}, CheckpointState{}, err
	}

	return Transcript{blocks: cloneCheckpointBlocks(checkpoint.data.Blocks)}, CheckpointState{data: checkpointStateData{
		Version:       checkpointStateVersion,
		Input:         checkpoint.data.Input,
		Cursor:        checkpoint.data.Cursor,
		History:       append([]string(nil), checkpoint.data.History...),
		ContextTokens: checkpoint.data.ContextTokens,
		QueuedInputs:  append([]string(nil), checkpoint.data.QueuedInputs...),
	}}, nil
}

func JoinCheckpoint(transcript Transcript, state CheckpointState) (Checkpoint, error) {
	if err := validateTranscriptBlocks(transcript.blocks); err != nil {
		return Checkpoint{}, err
	}
	if err := validateCheckpointStateData(state.data); err != nil {
		return Checkpoint{}, err
	}

	data := terminalCheckpointData{
		Version:       terminalCheckpointVersion,
		Blocks:        cloneCheckpointBlocks(transcript.blocks),
		Input:         state.data.Input,
		Cursor:        state.data.Cursor,
		History:       append([]string(nil), state.data.History...),
		ContextTokens: state.data.ContextTokens,
		QueuedInputs:  append([]string(nil), state.data.QueuedInputs...),
	}
	if err := validateTerminalCheckpointData(data); err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{data: data}, nil
}

func DiffTranscript(previous, next Transcript) (TranscriptDelta, bool) {
	common := min(len(previous.blocks), len(next.blocks))
	replaceFrom := 0
	for replaceFrom < common && checkpointBlocksEqual(previous.blocks[replaceFrom], next.blocks[replaceFrom]) {
		replaceFrom++
	}
	if replaceFrom == len(previous.blocks) && replaceFrom == len(next.blocks) {
		return TranscriptDelta{}, false
	}

	return TranscriptDelta{
		replaceFrom: replaceFrom,
		blocks:      cloneCheckpointBlocks(next.blocks[replaceFrom:]),
	}, true
}

func ApplyTranscriptDelta(previous Transcript, delta TranscriptDelta) (Transcript, error) {
	if delta.replaceFrom < 0 || delta.replaceFrom > len(previous.blocks) {
		return Transcript{}, errors.New("terminal: transcript delta replacement is out of range")
	}
	if err := validateTranscriptBlocks(delta.blocks); err != nil {
		return Transcript{}, err
	}

	blocks := make([]checkpointBlock, 0, delta.replaceFrom+len(delta.blocks))
	blocks = append(blocks, cloneCheckpointBlocks(previous.blocks[:delta.replaceFrom])...)
	blocks = append(blocks, cloneCheckpointBlocks(delta.blocks)...)
	return Transcript{blocks: blocks}, nil
}

func (transcript Transcript) BlockCount() int {
	return len(transcript.blocks)
}

func (state CheckpointState) MarshalJSON() ([]byte, error) {
	if err := validateCheckpointStateData(state.data); err != nil {
		return nil, err
	}
	return json.Marshal(state.data)
}

func (state *CheckpointState) UnmarshalJSON(encoded []byte) error {
	var data checkpointStateData
	if err := decodePersistenceValue(encoded, &data); err != nil {
		return fmt.Errorf("terminal: decode checkpoint state: %w", err)
	}
	if err := validateCheckpointStateData(data); err != nil {
		return err
	}
	state.data = cloneCheckpointStateData(data)
	return nil
}

func (delta TranscriptDelta) MarshalJSON() ([]byte, error) {
	if delta.replaceFrom < 0 {
		return nil, errors.New("terminal: transcript delta replacement is out of range")
	}
	if err := validateTranscriptBlocks(delta.blocks); err != nil {
		return nil, err
	}
	return json.Marshal(transcriptDeltaData{
		ReplaceFrom: delta.replaceFrom,
		Blocks:      cloneCheckpointBlocks(delta.blocks),
	})
}

func (delta *TranscriptDelta) UnmarshalJSON(encoded []byte) error {
	var data transcriptDeltaData
	if err := decodePersistenceValue(encoded, &data); err != nil {
		return fmt.Errorf("terminal: decode transcript delta: %w", err)
	}
	if data.ReplaceFrom < 0 {
		return errors.New("terminal: transcript delta replacement is out of range")
	}
	if err := validateTranscriptBlocks(data.Blocks); err != nil {
		return err
	}
	delta.replaceFrom = data.ReplaceFrom
	delta.blocks = cloneCheckpointBlocks(data.Blocks)
	return nil
}

func validateCheckpointStateData(state checkpointStateData) error {
	if state.Version != checkpointStateVersion {
		return fmt.Errorf("terminal: unsupported checkpoint state version %d", state.Version)
	}
	return validateTerminalCheckpointData(terminalCheckpointData{
		Version:       terminalCheckpointVersion,
		Input:         state.Input,
		Cursor:        state.Cursor,
		History:       state.History,
		ContextTokens: state.ContextTokens,
		QueuedInputs:  state.QueuedInputs,
	})
}

func validateTranscriptBlocks(blocks []checkpointBlock) error {
	return validateTerminalCheckpointData(terminalCheckpointData{
		Version: terminalCheckpointVersion,
		Blocks:  blocks,
	})
}

func cloneCheckpointStateData(data checkpointStateData) checkpointStateData {
	data.History = append([]string(nil), data.History...)
	data.QueuedInputs = append([]string(nil), data.QueuedInputs...)
	return data
}

func cloneCheckpointBlocks(blocks []checkpointBlock) []checkpointBlock {
	cloned := make([]checkpointBlock, len(blocks))
	for index, block := range blocks {
		block.Content = append([]checkpointContentPart(nil), block.Content...)
		block.Tool = block.Tool.Clone()
		cloned[index] = block
	}
	return cloned
}

func checkpointBlocksEqual(left, right checkpointBlock) bool {
	if left.Kind != right.Kind || left.Text != right.Text || left.ToolCallID != right.ToolCallID || left.ToolOutcome != right.ToolOutcome || !left.Tool.Equal(right.Tool) {
		return false
	}
	if len(left.Content) != len(right.Content) {
		return false
	}
	for index := range left.Content {
		if left.Content[index] != right.Content[index] {
			return false
		}
	}
	return true
}

func decodePersistenceValue(encoded []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
