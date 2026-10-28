package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const (
	maxTaskDescriptionBytes   = 500
	maxCompletionResultBytes  = 32 * 1024
	maxCompletionResultLines  = 1_000
	maxCompletionMessageBytes = 40 * 1024
)

var (
	errCanceled      = errors.New("subagent canceled by user")
	errSessionClosed = errors.New("subagent interrupted by session shutdown")
)

type Manager struct {
	run func(context.Context, Task, func(agent.Usage)) (string, error)

	ctx           context.Context
	cancel        context.CancelCauseFunc
	mu            sync.Mutex
	active        []*job
	inbox         []Completion
	nextID        uint64
	nextMessageID uint64
	closed        bool
	workers       sync.WaitGroup
	closeOnce     sync.Once
	status        chan Status
	changes       chan struct{}
	dirty         chan struct{}
}

type job struct {
	id      string
	task    Task
	ctx     context.Context
	cancel  context.CancelCauseFunc
	state   State
	started time.Time
	usage   agent.Usage
}

func (job *job) status() JobStatus {
	return JobStatus{
		ID:            job.id,
		Description:   job.task.Description,
		ModelProfile:  job.task.ModelProfile,
		ThinkingLevel: job.task.ThinkingLevel,
		State:         job.state,
		Started:       job.started,
		Usage:         job.usage,
	}
}

func NewManager(run func(ctx context.Context, task Task, progress func(agent.Usage)) (string, error)) *Manager {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Manager{
		run:     run,
		ctx:     ctx,
		cancel:  cancel,
		status:  make(chan Status, 1),
		changes: make(chan struct{}),
		dirty:   make(chan struct{}, 1),
	}
}

// StatusChanges coalesces status changes when the receiver falls behind.
func (m *Manager) StatusChanges() <-chan Status {
	return m.status
}

// CheckpointChanges coalesces checkpoint changes when the receiver falls behind.
func (m *Manager) CheckpointChanges() <-chan struct{} {
	return m.dirty
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel(errSessionClosed)
		m.signalChangeLocked()
		m.mu.Unlock()

		m.workers.Wait()

		m.mu.Lock()
		close(m.status)
		close(m.dirty)
		m.mu.Unlock()
	})
	return nil
}

func (m *Manager) Start(tasks []Task) ([]JobStatus, error) {
	if err := validateTasks(tasks); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("subagent manager is closed")
	}
	if requested := len(m.active) + len(tasks); requested > maxActive {
		return nil, fmt.Errorf("active subagents must not exceed %d (requested: %d)", maxActive, requested)
	}

	started := time.Now()
	statuses := make([]JobStatus, len(tasks))
	for index, task := range tasks {
		m.nextID++
		task.Description = truncateUTF8Lines(strings.TrimSpace(task.Description), maxTaskDescriptionBytes, 1)
		ctx, cancel := context.WithCancelCause(m.ctx)
		job := &job{
			id:      fmt.Sprintf("subagent-%d", m.nextID),
			task:    task,
			ctx:     ctx,
			cancel:  cancel,
			state:   StateRunning,
			started: started,
		}
		m.active = append(m.active, job)
		statuses[index] = job.status()
		m.workers.Go(func() { m.runJob(job) })
	}
	m.publishLocked()
	return statuses, nil
}

func validateTasks(tasks []Task) error {
	if len(tasks) == 0 {
		return errors.New("at least one task is required")
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.Description) == "" {
			return errors.New("task descriptions must be nonempty")
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return errors.New("task prompts must be nonempty")
		}
	}
	return nil
}

func (m *Manager) runJob(job *job) {
	text, runErr := m.run(job.ctx, job.task, func(usage agent.Usage) {
		m.mu.Lock()
		defer m.mu.Unlock()
		job.usage = usage
		m.publishLocked()
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	if cause := context.Cause(job.ctx); cause != nil {
		runErr = cause
	}
	job.cancel(nil)

	index := slices.Index(m.active, job)
	m.active = slices.Delete(m.active, index, index+1)
	m.nextMessageID++
	completion := Completion{
		MessageID:   m.nextMessageID,
		SubagentID:  job.id,
		Description: job.task.Description,
		Status:      completionState(runErr),
		Started:     job.started,
		Finished:    time.Now(),
		Result:      boundCompletionResult(text, runErr),
	}
	m.inbox = append(m.inbox, boundCompletion(completion))
	m.publishLocked()
}

func completionState(err error) State {
	switch {
	case errors.Is(err, errCanceled):
		return StateCanceled
	case errors.Is(err, errSessionClosed):
		return StateInterrupted
	case err != nil:
		return StateFailed
	default:
		return StateComplete
	}
}

func boundCompletionResult(text string, err error) string {
	if err != nil {
		if strings.TrimSpace(text) == "" {
			text = err.Error()
		} else {
			text = strings.TrimSpace(text) + "\n\nError: " + err.Error()
		}
	}
	return truncateUTF8Lines(strings.TrimSpace(text), maxCompletionResultBytes, maxCompletionResultLines)
}

func boundCompletion(completion Completion) Completion {
	encoded, _ := json.Marshal(completion)
	if len(encoded) <= maxCompletionMessageBytes {
		return completion
	}

	overhead := len(encoded) - len(completion.Result)
	completion.Result = truncateUTF8Lines(completion.Result, max(0, maxCompletionMessageBytes-overhead), maxCompletionResultLines)
	for {
		encoded, _ = json.Marshal(completion)
		if len(encoded) <= maxCompletionMessageBytes || completion.Result == "" {
			return completion
		}
		completion.Result = truncateUTF8Lines(completion.Result, max(0, len(completion.Result)-(len(encoded)-maxCompletionMessageBytes)), maxCompletionResultLines)
	}
}

func truncateUTF8Lines(text string, maxBytes, maxLines int) string {
	if text == "" || maxBytes <= 0 || maxLines <= 0 {
		return ""
	}

	end := lineEnd(text, maxLines)
	if end <= maxBytes && end == len(text) {
		return text
	}

	marker := "\n[truncated]"
	if maxLines == 1 {
		marker = " [truncated]"
	} else {
		end = min(end, lineEnd(text, maxLines-1))
	}
	if maxBytes <= len(marker) {
		return marker[:maxBytes]
	}
	end = min(end, maxBytes-len(marker))
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.TrimSpace(text[:end]) + marker
}

func lineEnd(text string, maxLines int) int {
	lines := 1
	for index, character := range text {
		if character != '\n' {
			continue
		}
		lines++
		if lines > maxLines {
			return index
		}
	}
	return len(text)
}

func (m *Manager) Cancel(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, errors.New("at least one subagent ID is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("subagent manager is closed")
	}
	jobs := make([]*job, len(ids))
	for index, id := range ids {
		position := slices.IndexFunc(m.active, func(job *job) bool { return job.id == id })
		if position < 0 {
			return nil, fmt.Errorf("subagent %q is not active", id)
		}
		jobs[index] = m.active[position]
	}

	canceled := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.state == StateCanceling {
			continue
		}
		job.state = StateCanceling
		job.cancel(errCanceled)
		canceled = append(canceled, job.id)
	}
	if len(canceled) > 0 {
		m.publishLocked()
	}
	return canceled, nil
}

func (m *Manager) Wait(ctx context.Context) (WaitOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	steering := agent.SteeringSignal(ctx)
	for {
		m.mu.Lock()
		switch {
		case len(m.inbox) > 0:
			m.mu.Unlock()
			return WaitCompletion, nil
		case len(m.active) == 0:
			m.mu.Unlock()
			return 0, errors.New("no active subagents or pending completions")
		default:
			changes := m.changes
			m.mu.Unlock()

			select {
			case <-ctx.Done():
				m.mu.Lock()
				completion := len(m.inbox) > 0
				m.mu.Unlock()
				switch {
				case completion:
					return WaitCompletion, nil
				case errors.Is(ctx.Err(), context.DeadlineExceeded):
					return WaitTimeout, nil
				default:
					return 0, ctx.Err()
				}
			case <-steering:
				return WaitSteering, nil
			case <-changes:
			}
		}
	}
}

func (m *Manager) Snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) AcknowledgeCompletions(messageIDs []uint64) error {
	if len(messageIDs) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inbox) < len(messageIDs) {
		return errors.New("subagent inbox acknowledgement exceeds pending messages")
	}
	for index, id := range messageIDs {
		if m.inbox[index].MessageID != id {
			return errors.New("subagent inbox acknowledgement does not match pending prefix")
		}
	}
	m.inbox = append([]Completion(nil), m.inbox[len(messageIDs):]...)
	if !m.closed {
		m.publishLocked()
	}
	return nil
}

func (m *Manager) publishLocked() {
	status := m.statusLocked()
	select {
	case m.status <- status:
	default:
		select {
		case <-m.status:
		default:
		}
		select {
		case m.status <- status:
		default:
		}
	}
	m.signalChangeLocked()
	select {
	case m.dirty <- struct{}{}:
	default:
	}
}

func (m *Manager) statusLocked() Status {
	active := make([]JobStatus, len(m.active))
	for index, job := range m.active {
		active[index] = job.status()
	}
	return Status{Active: active, PendingCompletions: slices.Clone(m.inbox)}
}

func (m *Manager) signalChangeLocked() {
	close(m.changes)
	m.changes = make(chan struct{})
}
