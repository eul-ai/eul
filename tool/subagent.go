package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	subagentToolName       = "subagent"
	maxSubagents           = 4
	subagentUpdateInterval = time.Second
)

var (
	errSubagentCanceled      = errors.New("subagent canceled by user")
	errSubagentSessionClosed = errors.New("subagent canceled by session shutdown")
)

type SubagentModelProfile string

const (
	SubagentModelFast           SubagentModelProfile = "fast"
	SubagentModelBalanced       SubagentModelProfile = "balanced"
	SubagentModelPowerful       SubagentModelProfile = "powerful"
	defaultSubagentModelProfile                      = SubagentModelBalanced
)

var subagentToolDefinition = agent.ToolDefinition{
	Name:        subagentToolName,
	Description: "Start one to four independent read-only research tasks in the background. At most four may be outstanding. Use selectively for substantial parallel investigation that benefits from separate context; do not use for trivial work, tightly coupled steps, or tasks the main context can handle directly. Choose the lowest model profile and thinking level sufficient for the tasks. Do not delegate follow-up work for findings already available in context.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"tasks": {
			Type:        "array",
			Description: "One to four independent tasks. Include all context each subagent needs.",
			Items:       &agent.JSONSchema{Type: "string"},
		},
		"model_profile": {
			Type:        "string",
			Description: "Model profile for every task: fast, balanced, or powerful. Defaults to balanced. Choose fast for simple lookups, balanced for most research, and powerful only for difficult analysis.",
		},
		"thinking_level": {
			Type:        "string",
			Description: "Thinking level for every task: off, minimal, low, medium, or high. Defaults to low. Choose the lowest sufficient level.",
		},
	}, "tasks"),
}

type SubagentProgress struct {
	Usage              agent.Usage
	Generations        int
	Finalizing         bool
	FinalizationReason agent.FinalizationReason
}

type SubagentRun func(context.Context, string, SubagentModelProfile, agent.ThinkingLevel, func(SubagentProgress)) (agent.RunResult, error)

type Subagent struct {
	run                     SubagentRun
	supportedThinkingLevels []agent.ThinkingLevel

	ctx       context.Context
	cancel    context.CancelCauseFunc
	mu        sync.Mutex
	jobs      map[string]*subagentJob
	nextID    uint64
	closed    bool
	workers   sync.WaitGroup
	closeOnce sync.Once
	status    chan agent.SubagentStatus
	changes   chan struct{}
}

type subagentArguments struct {
	Tasks         []string `json:"tasks"`
	ModelProfile  *string  `json:"model_profile"`
	ThinkingLevel *string  `json:"thinking_level"`
}

type subagentJob struct {
	id                 string
	task               string
	modelProfile       SubagentModelProfile
	thinkingLevel      agent.ThinkingLevel
	ctx                context.Context
	cancel             context.CancelCauseFunc
	state              string
	started            time.Time
	elapsed            time.Duration
	usage              agent.Usage
	generations        int
	finalizationReason agent.FinalizationReason
	result             agent.RunResult
	err                error
}

type subagentResult struct {
	text string
	err  error
}

type subagentStatus struct {
	state              string
	started            time.Time
	elapsed            time.Duration
	usage              agent.Usage
	generations        int
	finalizationReason agent.FinalizationReason
}

type subagentJobSnapshot struct {
	id            string
	task          string
	modelProfile  SubagentModelProfile
	thinkingLevel agent.ThinkingLevel
	status        subagentStatus
	result        subagentResult
}

func NewSubagent(run SubagentRun, supportedThinkingLevels ...agent.ThinkingLevel) *Subagent {
	if len(supportedThinkingLevels) == 0 {
		supportedThinkingLevels = []agent.ThinkingLevel{
			agent.ThinkingOff,
			agent.ThinkingMinimal,
			agent.ThinkingLow,
			agent.ThinkingMedium,
			agent.ThinkingHigh,
		}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Subagent{
		run:                     run,
		supportedThinkingLevels: slices.Clone(supportedThinkingLevels),
		ctx:                     ctx,
		cancel:                  cancel,
		jobs:                    make(map[string]*subagentJob),
		status:                  make(chan agent.SubagentStatus, 1),
		changes:                 make(chan struct{}, 1),
	}
}

func (*Subagent) Definition() agent.ToolDefinition {
	return subagentToolDefinition
}

func (*Subagent) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["tasks"].([]any)
	tasks := make([]string, 0, len(values))
	for _, value := range values {
		if task, ok := value.(string); ok {
			tasks = append(tasks, task)
		}
	}
	modelProfile := defaultSubagentModelProfile
	if value, ok := snapshot.Arguments["model_profile"].(string); ok {
		modelProfile = SubagentModelProfile(value)
	}
	thinkingLevel := agent.ThinkingLow
	if value, ok := snapshot.Arguments["thinking_level"].(string); ok {
		thinkingLevel = agent.ThinkingLevel(value)
	}
	return subagentLaunchPresentation(tasks, nil, modelProfile, thinkingLevel)
}

func (s *Subagent) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[subagentArguments](arguments)
	if err != nil {
		return errorResult(subagentToolName, err), nil
	}
	if err := validateSubagentTasks(args.Tasks); err != nil {
		return errorResult(subagentToolName, err), nil
	}
	modelProfile, err := resolveSubagentModelProfile(args.ModelProfile)
	if err != nil {
		return errorResult(subagentToolName, err), nil
	}
	thinkingLevel, err := s.resolveThinkingLevel(args.ThinkingLevel)
	if err != nil {
		return errorResult(subagentToolName, err), nil
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	jobs, err := s.start(args.Tasks, modelProfile, thinkingLevel)
	if err != nil {
		return errorResult(subagentToolName, err), nil
	}

	ids := make([]string, len(jobs))
	for index, job := range jobs {
		ids[index] = job.id
	}
	if updates != nil {
		updates.SetFinal(subagentLaunchPresentation(args.Tasks, ids, modelProfile, thinkingLevel))
	}
	return successResult(formatSubagentLaunches(jobs, modelProfile, thinkingLevel)), nil
}

func (s *Subagent) StatusUpdates() <-chan agent.SubagentStatus {
	return s.status
}

func (s *Subagent) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel(errSubagentSessionClosed)
		s.mu.Unlock()

		s.workers.Wait()
	})
	return nil
}

func validateSubagentTasks(tasks []string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("at least one task is required")
	}
	if len(tasks) > maxSubagents {
		return fmt.Errorf("tasks must not exceed %d", maxSubagents)
	}
	for _, task := range tasks {
		if strings.TrimSpace(task) == "" {
			return fmt.Errorf("tasks must be nonempty")
		}
	}
	return nil
}

func resolveSubagentModelProfile(value *string) (SubagentModelProfile, error) {
	profile := defaultSubagentModelProfile
	if value != nil {
		profile = SubagentModelProfile(*value)
	}

	switch profile {
	case SubagentModelFast, SubagentModelBalanced, SubagentModelPowerful:
		return profile, nil
	default:
		return "", fmt.Errorf("model profile must be one of fast, balanced, or powerful")
	}
}

func (s *Subagent) resolveThinkingLevel(value *string) (agent.ThinkingLevel, error) {
	level := agent.ThinkingLow
	if value != nil {
		level = agent.ThinkingLevel(*value)
	}

	switch level {
	case agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh:
	case agent.ThinkingXHigh, agent.ThinkingMax:
		return "", fmt.Errorf("thinking level %q is not available to subagents; use off, minimal, low, medium, or high", level)
	default:
		return "", fmt.Errorf("thinking level must be one of off, minimal, low, medium, or high")
	}
	if !slices.Contains(s.supportedThinkingLevels, level) {
		return "", fmt.Errorf("thinking level %q is not supported by the current model", level)
	}
	return level, nil
}

func (s *Subagent) start(tasks []string, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel) ([]*subagentJob, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("subagent manager is closed")
	}
	if len(s.jobs)+len(tasks) > maxSubagents {
		s.mu.Unlock()
		return nil, fmt.Errorf("outstanding subagents must not exceed %d", maxSubagents)
	}

	started := time.Now()
	jobs := make([]*subagentJob, len(tasks))
	for index, task := range tasks {
		s.nextID++
		jobCtx, cancel := context.WithCancelCause(s.ctx)
		job := &subagentJob{
			id:            fmt.Sprintf("subagent-%d", s.nextID),
			task:          task,
			modelProfile:  modelProfile,
			thinkingLevel: thinkingLevel,
			ctx:           jobCtx,
			cancel:        cancel,
			state:         "running",
			started:       started,
		}
		s.jobs[job.id] = job
		jobs[index] = job
	}

	s.workers.Add(len(jobs))
	s.publishStatusLocked()
	s.mu.Unlock()

	s.signalChange()
	for _, job := range jobs {
		go s.runJob(job)
	}
	return jobs, nil
}

func (s *Subagent) runJob(job *subagentJob) {
	defer s.workers.Done()

	result, runErr := s.run(job.ctx, job.task, job.modelProfile, job.thinkingLevel, func(progress SubagentProgress) {
		s.mu.Lock()
		stateChanged := false
		if !subagentStateTerminal(job.state) {
			if progress.Usage != (agent.Usage{}) {
				job.usage = progress.Usage
			}
			if progress.Generations > job.generations {
				job.generations = progress.Generations
			}
			if progress.Finalizing && job.state == "running" {
				job.state = "finalizing"
				job.finalizationReason = progress.FinalizationReason
				stateChanged = true
			}
		}
		if stateChanged {
			s.publishStatusLocked()
		}
		s.mu.Unlock()
		s.signalChange()
	})
	cause := context.Cause(job.ctx)
	job.cancel(nil)
	if cause != nil {
		runErr = cause
	}

	s.mu.Lock()
	job.elapsed = time.Since(job.started)
	job.result = result
	job.err = runErr
	if result.Usage.TotalTokens > 0 {
		job.usage = result.Usage
	}
	switch {
	case errors.Is(runErr, errSubagentCanceled), errors.Is(runErr, errSubagentSessionClosed):
		job.state = "canceled"
	case runErr != nil:
		job.state = "failed"
	default:
		job.state = "complete"
	}
	s.publishStatusLocked()
	s.mu.Unlock()
	s.signalChange()
}

func (s *Subagent) jobsForIDs(ids []string) ([]*subagentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, fmt.Errorf("subagent manager is closed")
	}

	jobs := make([]*subagentJob, len(ids))
	for index, id := range ids {
		job, exists := s.jobs[id]
		if !exists {
			return nil, fmt.Errorf("unknown or expired subagent ID %q", id)
		}
		jobs[index] = job
	}
	return jobs, nil
}

func (s *Subagent) cancelJobs(jobs []*subagentJob, cause error) []string {
	s.mu.Lock()
	canceled := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if subagentStateTerminal(job.state) || job.state == "canceling" {
			continue
		}
		job.state = "canceling"
		job.cancel(cause)
		canceled = append(canceled, job.id)
	}
	if len(canceled) > 0 {
		s.publishStatusLocked()
	}
	s.mu.Unlock()
	if len(canceled) > 0 {
		s.signalChange()
	}
	return canceled
}

func (s *Subagent) snapshotJobs(jobs []*subagentJob) ([]subagentJobSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots := make([]subagentJobSnapshot, len(jobs))
	complete := true
	for index, job := range jobs {
		snapshots[index] = subagentJobSnapshot{
			id:            job.id,
			task:          job.task,
			modelProfile:  job.modelProfile,
			thinkingLevel: job.thinkingLevel,
			status: subagentStatus{
				state:              job.state,
				started:            job.started,
				elapsed:            job.elapsed,
				usage:              job.usage,
				generations:        job.generations,
				finalizationReason: job.finalizationReason,
			},
			result: subagentResult{text: job.result.Text, err: job.err},
		}
		if !subagentStateTerminal(job.state) {
			complete = false
		}
	}
	return snapshots, complete
}

func (s *Subagent) snapshotsForPresentation(ids []string) []subagentJobSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots := make([]subagentJobSnapshot, len(ids))
	for index, id := range ids {
		snapshots[index] = subagentJobSnapshot{id: id, status: subagentStatus{state: "pending"}}
		job, exists := s.jobs[id]
		if !exists {
			continue
		}
		snapshots[index].task = job.task
		snapshots[index].modelProfile = job.modelProfile
		snapshots[index].thinkingLevel = job.thinkingLevel
		snapshots[index].status = subagentStatus{
			state:              job.state,
			started:            job.started,
			elapsed:            job.elapsed,
			usage:              job.usage,
			generations:        job.generations,
			finalizationReason: job.finalizationReason,
		}
	}
	return snapshots
}

func (s *Subagent) consume(jobs []*subagentJob) {
	s.mu.Lock()
	for _, job := range jobs {
		if s.jobs[job.id] == job {
			delete(s.jobs, job.id)
		}
	}
	s.publishStatusLocked()
	s.mu.Unlock()
	s.signalChange()
}

func (s *Subagent) publishStatusLocked() {
	status := agent.SubagentStatus{}
	for _, job := range s.jobs {
		switch job.state {
		case "finalizing":
			status.Finalizing++
		case "running", "canceling":
			status.Running++
		default:
			status.Completed++
		}
	}

	select {
	case s.status <- status:
		return
	default:
	}
	select {
	case <-s.status:
	default:
	}
	select {
	case s.status <- status:
	default:
	}
}

func (s *Subagent) signalChange() {
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

func subagentStateTerminal(state string) bool {
	switch state {
	case "complete", "failed", "canceled":
		return true
	default:
		return false
	}
}

func formatSubagentLaunches(jobs []*subagentJob, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Started subagents (model: %s, thinking: %s):", modelProfile, thinkingLevel)
	for _, job := range jobs {
		label := boundPresentationLabel(strings.TrimSpace(strings.SplitN(job.task, "\n", 2)[0]), 120)
		fmt.Fprintf(&output, "\n- %s: %s", job.id, label)
	}
	return output.String()
}

func subagentLaunchPresentation(tasks, ids []string, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Title: subagentToolName, Markdown: true}
	if len(tasks) > 1 {
		presentation.Arguments = fmt.Sprintf("(%d, %s, %s)", len(tasks), modelProfile, thinkingLevel)
	} else if len(tasks) == 1 {
		presentation.Arguments = fmt.Sprintf("(%s, %s)", modelProfile, thinkingLevel)
	}
	presentation.Lines = make([]string, len(tasks))
	for index, task := range tasks {
		state := "pending"
		if index < len(ids) {
			state = "started (" + ids[index] + ")"
		}
		label := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
		label = boundPresentationLabel(label, 120)
		presentation.Lines[index] = fmt.Sprintf("%d. %s — %s", index+1, state, label)
	}
	return presentation
}

func formatSubagentStatus(status subagentStatus, now time.Time) string {
	if status.state == "pending" {
		return status.state
	}

	elapsed := status.elapsed
	if !subagentStateTerminal(status.state) {
		elapsed = now.Sub(status.started)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	details := []string{elapsed.Truncate(time.Second).String()}
	switch {
	case status.usage.InputTokens > 0 || status.usage.OutputTokens > 0:
		details = append(
			details,
			formatSubagentTokenCount(status.usage.InputTokens)+" input",
			formatSubagentTokenCount(status.usage.OutputTokens)+" output",
		)
	case status.usage.TotalTokens > 0:
		details = append(details, formatSubagentTokenCount(status.usage.TotalTokens)+" processed")
	}
	details = append(details, fmt.Sprintf("%d/%d generations", status.generations, subagentFinalizeAfterGenerations))

	state := status.state
	if reason := formatFinalizationReason(status.finalizationReason); reason != "" {
		switch status.state {
		case "finalizing":
			state += " — " + reason
		case "complete":
			state += " — finalized by " + reason
		case "failed":
			state += " — finalization after " + reason
		}
	}
	return fmt.Sprintf("%s (%s)", state, strings.Join(details, ", "))
}

func formatSubagentTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(tokens)/1_000_000), ".0") + "m"
	case tokens >= 1_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(tokens)/1_000), ".0") + "k"
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func formatFinalizationReason(reason agent.FinalizationReason) string {
	switch reason {
	case agent.FinalizationReasonDuration:
		return "time limit"
	case agent.FinalizationReasonGenerations:
		return "generation limit"
	default:
		return ""
	}
}

func boundPresentationLabel(label string, maximum int) string {
	if len(label) <= maximum {
		return label
	}
	bounded, _ := truncateLine(label, maximum-3)
	return bounded + "..."
}
