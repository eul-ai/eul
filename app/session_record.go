package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

const (
	sessionStateVersion        = 1
	maxSessionDescriptionBytes = 120
)

type sessionStatus string

const (
	sessionIdle   sessionStatus = "idle"
	sessionActive sessionStatus = "active"
)

type sessionMetadata struct {
	Version          int                 `json:"version"`
	ID               string              `json:"id"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	Status           sessionStatus       `json:"status"`
	Provider         string              `json:"provider"`
	WorkingDirectory string              `json:"working_directory"`
	Model            string              `json:"model"`
	FastModel        string              `json:"fast_model"`
	BalancedModel    string              `json:"balanced_model"`
	ThinkingLevel    agent.ThinkingLevel `json:"thinking_level"`
	FastMode         bool                `json:"fast_mode,omitempty"`
	Description      string              `json:"description,omitempty"`
}

type sessionRecord struct {
	sessionMetadata
	Agent    agent.Checkpoint
	Subagent subagent.Checkpoint
	Terminal terminal.Checkpoint
}

type sessionState struct {
	sessionMetadata
	Agent      agent.Checkpoint         `json:"agent"`
	Subagent   subagent.Checkpoint      `json:"subagent"`
	Terminal   terminal.CheckpointState `json:"terminal"`
	Transcript sessionTranscriptHead    `json:"transcript"`
}

type sessionTranscriptHead struct {
	Slot       string `json:"slot"`
	Bytes      int64  `json:"bytes"`
	BlockCount int    `json:"block_count"`
	BaseBytes  int64  `json:"base_bytes"`
	DeltaBytes int64  `json:"delta_bytes"`
}

func (record sessionRecord) models() modelSet {
	return modelSet{
		main:     record.Model,
		fast:     record.FastModel,
		balanced: record.BalancedModel,
	}
}

type sessionSummary struct {
	ID          string
	Description string
	UpdatedAt   time.Time
	Active      bool
}

func encodeSessionState(state sessionState) ([]byte, error) {
	if err := validateSessionState(state); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode session state: %w", err)
	}
	return append(encoded, '\n'), nil
}

func decodeSessionState(encoded []byte) (sessionState, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	var state sessionState
	if err := decoder.Decode(&state); err != nil {
		return sessionState{}, fmt.Errorf("decode session state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return sessionState{}, errors.New("decode session state: multiple JSON values")
		}
		return sessionState{}, fmt.Errorf("decode session state: %w", err)
	}
	if err := validateSessionState(state); err != nil {
		return sessionState{}, err
	}
	return state, nil
}

func decodeSessionSummary(source io.Reader) (sessionSummary, string, error) {
	decoder := json.NewDecoder(source)
	opening, err := decoder.Token()
	if err != nil {
		return sessionSummary{}, "", fmt.Errorf("decode session state: %w", err)
	}
	if opening != json.Delim('{') {
		return sessionSummary{}, "", errors.New("decode session state: expected object")
	}

	var metadata sessionMetadata
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return sessionSummary{}, "", fmt.Errorf("decode session state: %w", err)
		}
		name, ok := key.(string)
		if !ok {
			return sessionSummary{}, "", errors.New("decode session state: expected field name")
		}

		recognized, err := decodeSessionMetadataField(decoder, &metadata, name)
		if err != nil {
			return sessionSummary{}, "", fmt.Errorf("decode session state: %w", err)
		}
		if recognized {
			if name != "description" {
				continue
			}
			if err := validateSessionSummary(metadata); err != nil {
				return sessionSummary{}, "", err
			}
			return sessionSummary{
				ID:          metadata.ID,
				Description: metadata.Description,
				UpdatedAt:   metadata.UpdatedAt,
				Active:      metadata.Status == sessionActive,
			}, metadata.WorkingDirectory, nil
		}

		switch name {
		case "agent", "subagent", "terminal", "transcript":
			if err := metadata.validate(); err != nil {
				return sessionSummary{}, "", err
			}
			return sessionSummary{}, "", errors.New("session description is missing")
		default:
			return sessionSummary{}, "", fmt.Errorf("decode session state: unknown field %q before description", name)
		}
	}

	if err := metadata.validate(); err != nil {
		return sessionSummary{}, "", err
	}
	return sessionSummary{}, "", errors.New("session description is missing")
}

func decodeSessionMetadataField(decoder *json.Decoder, metadata *sessionMetadata, name string) (bool, error) {
	switch name {
	case "version":
		return true, decoder.Decode(&metadata.Version)
	case "id":
		return true, decoder.Decode(&metadata.ID)
	case "created_at":
		return true, decoder.Decode(&metadata.CreatedAt)
	case "updated_at":
		return true, decoder.Decode(&metadata.UpdatedAt)
	case "status":
		return true, decoder.Decode(&metadata.Status)
	case "provider":
		return true, decoder.Decode(&metadata.Provider)
	case "working_directory":
		return true, decoder.Decode(&metadata.WorkingDirectory)
	case "model":
		return true, decoder.Decode(&metadata.Model)
	case "fast_model":
		return true, decoder.Decode(&metadata.FastModel)
	case "balanced_model":
		return true, decoder.Decode(&metadata.BalancedModel)
	case "thinking_level":
		return true, decoder.Decode(&metadata.ThinkingLevel)
	case "fast_mode":
		return true, decoder.Decode(&metadata.FastMode)
	case "description":
		return true, decoder.Decode(&metadata.Description)
	default:
		return false, nil
	}
}

func validateSessionSummary(metadata sessionMetadata) error {
	if err := metadata.validate(); err != nil {
		return err
	}
	return validateSessionDescription(metadata.Description, true)
}

func (metadata sessionMetadata) validate() error {
	switch {
	case metadata.Version != sessionStateVersion:
		return fmt.Errorf("unsupported session state version %d", metadata.Version)
	case !validSessionID(metadata.ID):
		return errors.New("session has an invalid ID")
	case metadata.CreatedAt.IsZero() || metadata.UpdatedAt.IsZero():
		return errors.New("session timestamps are missing")
	case metadata.UpdatedAt.Before(metadata.CreatedAt):
		return errors.New("session timestamps are inconsistent")
	case metadata.Status != sessionIdle && metadata.Status != sessionActive:
		return fmt.Errorf("session has invalid status %q", metadata.Status)
	case !backend.ValidID(metadata.Provider):
		return fmt.Errorf("session provider %q is invalid", metadata.Provider)
	case !filepath.IsAbs(metadata.WorkingDirectory) || filepath.Clean(metadata.WorkingDirectory) != metadata.WorkingDirectory:
		return errors.New("session working directory is not canonical")
	case strings.TrimSpace(metadata.Model) == "":
		return errors.New("session model is empty")
	case strings.TrimSpace(metadata.FastModel) == "":
		return errors.New("session fast model is empty")
	case strings.TrimSpace(metadata.BalancedModel) == "":
		return errors.New("session balanced model is empty")
	case !metadata.ThinkingLevel.Valid():
		return errors.New("session thinking level is invalid")
	}
	return nil
}

func validateSessionState(state sessionState) error {
	if err := state.sessionMetadata.validate(); err != nil {
		return err
	}
	if !state.Agent.Initialized() || !state.Subagent.Initialized() {
		return errors.New("session checkpoints are missing")
	}
	if err := validateSessionDescription(state.Description, true); err != nil {
		return err
	}
	return state.Transcript.validate()
}

func (head sessionTranscriptHead) validate() error {
	switch {
	case head.Slot != "a" && head.Slot != "b":
		return errors.New("session transcript slot is invalid")
	case head.Bytes < 0 || head.BlockCount < 0 || head.BaseBytes < 0 || head.DeltaBytes < 0:
		return errors.New("session transcript head is invalid")
	case head.BaseBytes+head.DeltaBytes != head.Bytes:
		return errors.New("session transcript byte counts are inconsistent")
	case head.Bytes == 0 && head.BlockCount != 0:
		return errors.New("session transcript block count is inconsistent")
	}
	return nil
}

func validateSessionRecord(record sessionRecord) error {
	if err := record.sessionMetadata.validate(); err != nil {
		return err
	}
	if !record.Agent.Initialized() || !record.Subagent.Initialized() || !record.Terminal.Initialized() {
		return errors.New("session checkpoints are missing")
	}
	return validateSessionDescription(record.Description, false)
}

func validateSessionDescription(description string, required bool) error {
	if (required && description == "") || !utf8.ValidString(description) || len(description) > maxSessionDescriptionBytes || strings.IndexFunc(description, unicode.IsControl) >= 0 {
		return errors.New("session description is invalid")
	}
	return nil
}
