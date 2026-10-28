package session

import (
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

type Models struct {
	Main     string `json:"main"`
	Fast     string `json:"fast"`
	Balanced string `json:"balanced"`
}

type Settings struct {
	Provider         string              `json:"provider"`
	WorkingDirectory string              `json:"working_directory"`
	Models           Models              `json:"models"`
	ThinkingLevel    agent.ThinkingLevel `json:"thinking_level"`
	FastMode         bool                `json:"fast_mode,omitempty"`
}

type Draft struct {
	Input        string   `json:"input,omitempty"`
	Cursor       int      `json:"cursor,omitempty"`
	QueuedInputs []string `json:"queued_inputs,omitempty"`
}

type Summary struct {
	ID          string
	Description string
	UpdatedAt   time.Time
	Active      bool
}

type metadata struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Active    bool      `json:"active,omitempty"`
	Settings
	Description string `json:"description,omitempty"`
}

type state struct {
	Agent      agent.Checkpoint    `json:"agent"`
	Subagent   subagent.Checkpoint `json:"subagent"`
	Transcript []Entry             `json:"transcript,omitempty"`
	Draft      Draft               `json:"draft,omitzero"`
}

type Record struct {
	metadata
	state
}
