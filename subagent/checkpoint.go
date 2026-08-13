package subagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
)

const checkpointVersion = 1

type Checkpoint struct {
	data checkpointData
}

type checkpointData struct {
	Version       int                `json:"version"`
	NextID        uint64             `json:"next_id"`
	NextMessageID uint64             `json:"next_message_id"`
	Active        []activeCheckpoint `json:"active,omitempty"`
	Inbox         []Completion       `json:"inbox,omitempty"`
}

type activeCheckpoint struct {
	ID            string              `json:"id"`
	Order         uint64              `json:"order"`
	Description   string              `json:"description"`
	ModelProfile  Profile             `json:"model_profile"`
	ThinkingLevel agent.ThinkingLevel `json:"thinking_level"`
	State         State               `json:"state"`
	Started       time.Time           `json:"started"`
}

func EmptyCheckpoint() Checkpoint {
	return Checkpoint{data: checkpointData{Version: checkpointVersion}}
}

func (checkpoint Checkpoint) Initialized() bool {
	return checkpoint.data.Version == checkpointVersion
}

func (checkpoint Checkpoint) MarshalJSON() ([]byte, error) {
	data := cloneCheckpointData(checkpoint.data)
	if err := validateCheckpointData(data); err != nil {
		return nil, err
	}
	return json.Marshal(data)
}

func (checkpoint *Checkpoint) UnmarshalJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	var data checkpointData
	if err := decoder.Decode(&data); err != nil {
		return fmt.Errorf("subagent: decode checkpoint: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("subagent: decode checkpoint: multiple JSON values")
		}
		return fmt.Errorf("subagent: decode checkpoint: %w", err)
	}
	if err := validateCheckpointData(data); err != nil {
		return err
	}
	checkpoint.data = cloneCheckpointData(data)
	return nil
}

func validateCheckpointData(data checkpointData) error {
	if data.Version != checkpointVersion {
		return fmt.Errorf("subagent: unsupported checkpoint version %d", data.Version)
	}
	seenJobs := make(map[string]struct{}, len(data.Active)+len(data.Inbox))
	for _, active := range data.Active {
		if strings.TrimSpace(active.ID) == "" || active.Order == 0 || strings.TrimSpace(active.Description) == "" || len(active.Description) > maxTaskDescriptionBytes {
			return errors.New("subagent: checkpoint contains invalid active job")
		}
		if active.Order > data.NextID {
			return errors.New("subagent: active job exceeds ID high-water mark")
		}
		if _, exists := seenJobs[active.ID]; exists {
			return fmt.Errorf("subagent: duplicate active job %q", active.ID)
		}
		seenJobs[active.ID] = struct{}{}
		switch active.State {
		case StateRunning, StateCanceling:
		default:
			return fmt.Errorf("subagent: invalid active state %q", active.State)
		}
	}
	lastMessageID := uint64(0)
	for _, completion := range data.Inbox {
		if completion.MessageID <= lastMessageID || completion.MessageID > data.NextMessageID || strings.TrimSpace(completion.SubagentID) == "" {
			return errors.New("subagent: checkpoint contains invalid completion sequence")
		}
		if _, exists := seenJobs[completion.SubagentID]; exists {
			return fmt.Errorf("subagent: duplicate job %q", completion.SubagentID)
		}
		seenJobs[completion.SubagentID] = struct{}{}
		encoded, err := json.Marshal(completion)
		if err != nil || len(encoded) > maxCompletionMessageBytes {
			return errors.New("subagent: checkpoint contains an oversized completion")
		}
		switch completion.Status {
		case StateComplete, StateFailed, StateCanceled, StateInterrupted:
		default:
			return fmt.Errorf("subagent: invalid completion state %q", completion.Status)
		}
		lastMessageID = completion.MessageID
	}
	return nil
}

func cloneCheckpointData(data checkpointData) checkpointData {
	data.Active = slices.Clone(data.Active)
	data.Inbox = slices.Clone(data.Inbox)
	return data
}

func (m *Manager) Checkpoint() Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobs := m.sortedJobsLocked()
	active := make([]activeCheckpoint, len(jobs))
	for index, job := range jobs {
		active[index] = activeCheckpoint{
			ID:            job.id,
			Order:         job.order,
			Description:   job.description,
			ModelProfile:  job.modelProfile,
			ThinkingLevel: job.thinkingLevel,
			State:         job.state,
			Started:       job.started,
		}
	}
	return Checkpoint{data: checkpointData{
		Version:       checkpointVersion,
		NextID:        m.nextID,
		NextMessageID: m.nextMessageID,
		Active:        active,
		Inbox:         append([]Completion(nil), m.inbox...),
	}}
}

func (m *Manager) RestoreCheckpoint(checkpoint Checkpoint) error {
	data := cloneCheckpointData(checkpoint.data)
	if err := validateCheckpointData(data); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("subagent manager is closed")
	}
	if len(m.active) > 0 || len(m.inbox) > 0 || m.nextID != 0 || m.nextMessageID != 0 {
		return errors.New("subagent manager already has state")
	}

	m.nextID = data.NextID
	m.nextMessageID = data.NextMessageID
	m.inbox = append([]Completion(nil), data.Inbox...)
	for _, active := range data.Active {
		m.nextMessageID++
		m.inbox = append(m.inbox, boundCompletion(Completion{
			MessageID:  m.nextMessageID,
			SubagentID: active.ID,
			Task:       active.Description,
			Status:     StateInterrupted,
			Started:    active.Started,
			Finished:   time.Now(),
			Result:     "Subagent execution was interrupted when the previous session ended.",
		}))
	}
	m.publishLocked()
	return nil
}
