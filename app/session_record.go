package app

import (
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

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
