package subagent

import (
	"slices"
	"time"
)

type Checkpoint struct {
	NextID      uint64       `json:"next_id"`
	Completions []Completion `json:"completions,omitempty"`
}

// Checkpoint records active jobs as interrupted completions, because a
// restored session cannot resume them.
func (m *Manager) Checkpoint() Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()

	completions := slices.Clone(m.inbox)
	messageID := m.nextMessageID
	for _, job := range m.active {
		messageID++
		completions = append(completions, Completion{
			MessageID:   messageID,
			SubagentID:  job.id,
			Description: job.task.Description,
			Status:      StateInterrupted,
			Started:     job.started,
			Finished:    time.Now(),
			Result:      "Subagent execution was interrupted when the previous session ended.",
		})
	}
	return Checkpoint{NextID: m.nextID, Completions: completions}
}
