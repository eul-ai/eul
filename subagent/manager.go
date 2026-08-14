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
	runner                  Runner
	supportedThinkingLevels func(Profile) []agent.ThinkingLevel
	defaultThinkingLevel    func() agent.ThinkingLevel

	ctx           context.Context
	cancel        context.CancelCauseFunc
	mu            sync.Mutex
	active        map[string]*job
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
	id            string
	order         uint64
	description   string
	task          string
	modelProfile  Profile
	thinkingLevel agent.ThinkingLevel
	ctx           context.Context
	cancel        context.CancelCauseFunc
	state         State
	started       time.Time
	usage         agent.Usage
}

func NewManager(config Config) *Manager {
	supportedThinkingLevels := config.SupportedThinkingLevels
	if supportedThinkingLevels == nil {
		supportedThinkingLevels = func(Profile) []agent.ThinkingLevel {
			return agent.ThinkingLevels()
		}
	}
	defaultThinkingLevel := config.DefaultThinkingLevel
	if defaultThinkingLevel == nil {
		defaultThinkingLevel = func() agent.ThinkingLevel {
			return agent.DefaultThinkingLevel
		}
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	return &Manager{
		runner:                  config.Runner,
		supportedThinkingLevels: supportedThinkingLevels,
		defaultThinkingLevel:    defaultThinkingLevel,
		ctx:                     ctx,
		cancel:                  cancel,
		active:                  make(map[string]*job),
		status:                  make(chan Status, 1),
		changes:                 make(chan struct{}),
		dirty:                   make(chan struct{}, 1),
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

func (m *Manager) Start(tasks []Task) ([]Job, error) {
	tasks = m.normalizeTasks(tasks)
	if err := m.validateStart(tasks); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("subagent manager is closed")
	}
	if len(m.active)+len(tasks) > maxActive {
		m.mu.Unlock()
		return nil, fmt.Errorf("active subagents must not exceed %d", maxActive)
	}

	started := time.Now()
	jobs := make([]*job, len(tasks))
	for index, task := range tasks {
		m.nextID++
		jobCtx, cancel := context.WithCancelCause(m.ctx)
		job := &job{
			id:            fmt.Sprintf("subagent-%d", m.nextID),
			order:         m.nextID,
			description:   truncateUTF8Lines(strings.TrimSpace(task.Description), maxTaskDescriptionBytes, 1),
			task:          task.Prompt,
			modelProfile:  task.ModelProfile,
			thinkingLevel: task.ThinkingLevel,
			ctx:           jobCtx,
			cancel:        cancel,
			state:         StateRunning,
			started:       started,
		}
		m.active[job.id] = job
		jobs[index] = job
	}

	m.workers.Add(len(jobs))
	m.publishLocked()
	m.mu.Unlock()

	startedJobs := make([]Job, len(jobs))
	for index, job := range jobs {
		startedJobs[index] = Job{
			ID:            job.id,
			Description:   job.description,
			ModelProfile:  job.modelProfile,
			ThinkingLevel: job.thinkingLevel,
		}
		go m.runJob(job)
	}
	return startedJobs, nil
}

func (m *Manager) normalizeTasks(tasks []Task) []Task {
	normalized := slices.Clone(tasks)
	for index := range normalized {
		if normalized[index].ModelProfile == "" {
			normalized[index].ModelProfile = ProfileMain
		}
		if normalized[index].ThinkingLevel == "" {
			normalized[index].ThinkingLevel = agent.ClampThinkingLevel(m.defaultThinkingLevel(), m.supportedThinkingLevels(normalized[index].ModelProfile))
		}
	}
	return normalized
}

func (m *Manager) validateStart(tasks []Task) error {
	if len(tasks) == 0 {
		return errors.New("at least one task is required")
	}
	if len(tasks) > maxActive {
		return fmt.Errorf("tasks must not exceed %d", maxActive)
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.Description) == "" {
			return errors.New("task descriptions must be nonempty")
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return errors.New("task prompts must be nonempty")
		}
		if !task.ModelProfile.valid() {
			return errors.New("model profile must be one of fast, balanced, or main")
		}
		if err := validateThinkingLevel(task.ThinkingLevel); err != nil {
			return err
		}
		if !slices.Contains(m.supportedThinkingLevels(task.ModelProfile), task.ThinkingLevel) {
			return fmt.Errorf("thinking level %q is not supported by the %s model", task.ThinkingLevel, task.ModelProfile)
		}
	}
	return nil
}

func validateThinkingLevel(level agent.ThinkingLevel) error {
	if level.Valid() {
		return nil
	}

	return fmt.Errorf("thinking level must be one of %s", formatThinkingLevels(agent.ThinkingLevels()))
}

func formatThinkingLevels(levels []agent.ThinkingLevel) string {
	values := make([]string, len(levels))
	for index, level := range levels {
		values[index] = string(level)
	}
	if len(values) < 2 {
		return strings.Join(values, "")
	}
	return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
}

func (m *Manager) runJob(job *job) {
	defer m.workers.Done()

	request := RunRequest{Task: job.task, Profile: job.modelProfile, ThinkingLevel: job.thinkingLevel}
	result, runErr := m.runner.Run(job.ctx, request, func(progress Progress) {
		m.mu.Lock()
		if m.active[job.id] == job && progress.Usage != (agent.Usage{}) {
			job.usage = progress.Usage
			m.publishLocked()
		}
		m.mu.Unlock()
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[job.id] != job {
		return
	}
	cause := context.Cause(job.ctx)
	job.cancel(nil)
	if cause != nil {
		runErr = cause
	}

	delete(m.active, job.id)
	m.nextMessageID++
	completion := Completion{
		MessageID:  m.nextMessageID,
		SubagentID: job.id,
		Task:       strings.TrimSpace(job.description),
		Status:     completionState(runErr),
		Started:    job.started,
		Finished:   time.Now(),
		Result:     boundCompletionResult(result.Text, runErr),
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
	if err := validateCancellationIDs(ids); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, errors.New("subagent manager is closed")
	}
	jobs := make([]*job, len(ids))
	for index, id := range ids {
		job, ok := m.active[id]
		if !ok {
			return nil, fmt.Errorf("subagent %q is not active", id)
		}
		jobs[index] = job
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

func validateCancellationIDs(ids []string) error {
	if len(ids) == 0 {
		return errors.New("at least one subagent ID is required")
	}
	if len(ids) > maxActive {
		return fmt.Errorf("subagent IDs must not exceed %d", maxActive)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errors.New("subagent IDs must be nonempty")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate subagent ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
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
	m.publishLocked()
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
	status := Status{PendingCompletions: append([]Completion(nil), m.inbox...)}
	jobs := m.sortedJobsLocked()
	status.Active = make([]JobStatus, len(jobs))
	status.Running = len(jobs)
	for index, job := range jobs {
		status.Active[index] = JobStatus{
			ID:            job.id,
			Task:          job.description,
			ModelProfile:  job.modelProfile,
			ThinkingLevel: job.thinkingLevel,
			State:         job.state,
			Started:       job.started,
			Usage:         job.usage,
		}
	}
	return status
}

func (m *Manager) sortedJobsLocked() []*job {
	jobs := make([]*job, 0, len(m.active))
	for _, job := range m.active {
		jobs = append(jobs, job)
	}
	slices.SortFunc(jobs, func(left, right *job) int {
		return int(left.order) - int(right.order)
	})
	return jobs
}

func (m *Manager) signalChangeLocked() {
	close(m.changes)
	m.changes = make(chan struct{})
}
