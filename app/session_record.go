package app

import (
	"errors"
	"fmt"
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

const maxSessionDescriptionBytes = 120

type sessionStatus string

const (
	sessionIdle   sessionStatus = "idle"
	sessionActive sessionStatus = "active"
)

type sessionMetadata struct {
	ID               string              `json:"id"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	Status           sessionStatus       `json:"status"`
	Provider         string              `json:"provider"`
	WorkingDirectory string              `json:"working_directory"`
	Models           backend.Models      `json:"models"`
	ThinkingLevel    agent.ThinkingLevel `json:"thinking_level"`
	FastMode         bool                `json:"fast_mode,omitempty"`
	Description      string              `json:"description,omitempty"`
}

type sessionState struct {
	Agent    agent.Checkpoint    `json:"agent"`
	Subagent subagent.Checkpoint `json:"subagent"`
	Terminal terminal.Checkpoint `json:"terminal"`
}

type sessionRecord struct {
	sessionMetadata
	sessionState
}

func (record sessionRecord) settings() sessionSettings {
	return sessionSettings{
		provider:      record.Provider,
		models:        record.Models,
		thinkingLevel: record.ThinkingLevel,
		fastMode:      record.FastMode,
		cwd:           record.WorkingDirectory,
	}
}

func (metadata sessionMetadata) summary() terminal.SessionSummary {
	return terminal.SessionSummary{
		ID:          metadata.ID,
		Description: metadata.Description,
		UpdatedAt:   metadata.UpdatedAt,
		Active:      metadata.Status == sessionActive,
	}
}

func (metadata sessionMetadata) validate() error {
	switch {
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
