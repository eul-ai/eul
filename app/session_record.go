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
	sessionRecordVersion       = 3
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
	Revision         uint64              `json:"revision"`
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
	Agent    agent.Checkpoint    `json:"agent"`
	Subagent subagent.Checkpoint `json:"subagent"`
	Terminal terminal.Checkpoint `json:"terminal"`
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

func encodeSessionRecord(record sessionRecord) ([]byte, error) {
	if err := validateSessionRecord(record); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode session: %w", err)
	}
	return append(encoded, '\n'), nil
}

func decodeSessionRecord(encoded []byte) (sessionRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	var record sessionRecord
	if err := decoder.Decode(&record); err != nil {
		return sessionRecord{}, fmt.Errorf("decode session: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return sessionRecord{}, errors.New("decode session: multiple JSON values")
		}
		return sessionRecord{}, fmt.Errorf("decode session: %w", err)
	}
	if err := validateSessionRecord(record); err != nil {
		return sessionRecord{}, err
	}
	return record, nil
}

func decodeSessionSummary(source io.Reader) (sessionSummary, string, error) {
	decoder := json.NewDecoder(source)
	opening, err := decoder.Token()
	if err != nil {
		return sessionSummary{}, "", fmt.Errorf("decode session: %w", err)
	}
	if opening != json.Delim('{') {
		return sessionSummary{}, "", errors.New("decode session: expected object")
	}

	var metadata sessionMetadata
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return sessionSummary{}, "", fmt.Errorf("decode session: %w", err)
		}
		name, ok := key.(string)
		if !ok {
			return sessionSummary{}, "", errors.New("decode session: expected field name")
		}

		recognized, err := decodeSessionMetadataField(decoder, &metadata, name)
		if err != nil {
			return sessionSummary{}, "", fmt.Errorf("decode session: %w", err)
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
		case "agent", "subagent", "terminal":
			if err := metadata.validate(); err != nil {
				return sessionSummary{}, "", err
			}
			return sessionSummary{}, "", errors.New("session description is missing")
		default:
			return sessionSummary{}, "", fmt.Errorf("decode session: unknown field %q before description", name)
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
	case "revision":
		return true, decoder.Decode(&metadata.Revision)
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
	case metadata.Version != sessionRecordVersion:
		return fmt.Errorf("unsupported session version %d", metadata.Version)
	case !validSessionID(metadata.ID):
		return errors.New("session has an invalid ID")
	case metadata.Revision == 0:
		return errors.New("session has no revision")
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
