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
	subagentToolName = "subagent"
	maxSubagents     = 4
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

var subagentTaskSchema = strictObject(map[string]agent.JSONSchema{
	"description": {
		Type:        "string",
		Description: "A short description of the task for progress display.",
	},
	"prompt": {
		Type:        "string",
		Description: "The complete task prompt. Include all context the subagent needs.",
	},
}, "description", "prompt")

var subagentToolDefinition = agent.ToolDefinition{
	Name:        subagentToolName,
	Description: "Launch one to four independent read-only research tasks, with at most four outstanding. Use only for worthwhile parallel investigation; choose the lowest sufficient profile and thinking level. Do not delegate follow-up work for findings already in context.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"tasks": {
			Type:        "array",
			Description: "One to four independent tasks.",
			Items:       &subagentTaskSchema,
		},
		"model_profile": {
			Type:        "string",
			Description: "fast, balanced (default), or powerful.",
		},
		"thinking_level": {
			Type:        "string",
			Description: "off, minimal, low (default), medium, or high.",
		},
	}, "tasks"),
}

type SubagentProgress struct {
	Usage       agent.Usage
	Generations int
	Finalizing  bool
}

type SubagentRun func(context.Context, string, SubagentModelProfile, agent.ThinkingLevel, func(SubagentProgress)) (agent.RunResult, error)

type Subagent struct {
	run                     SubagentRun
	supportedThinkingLevels func(SubagentModelProfile) []agent.ThinkingLevel

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
	Tasks         []subagentTask `json:"tasks"`
	ModelProfile  *string        `json:"model_profile"`
	ThinkingLevel *string        `json:"thinking_level"`
}

type subagentTask struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

type subagentJob struct {
	id            string
	order         uint64
	description   string
	task          string
	modelProfile  SubagentModelProfile
	thinkingLevel agent.ThinkingLevel
	ctx           context.Context
	cancel        context.CancelCauseFunc
	state         agent.SubagentState
	started       time.Time
	finished      time.Time
	usage         agent.Usage
	generations   int
	result        agent.RunResult
	err           error
}

type subagentResult struct {
	text string
	err  error
}

type subagentJobSnapshot struct {
	id            string
	modelProfile  SubagentModelProfile
	thinkingLevel agent.ThinkingLevel
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
	levels := slices.Clone(supportedThinkingLevels)
	return NewSubagentWithThinkingLevels(run, func(SubagentModelProfile) []agent.ThinkingLevel {
		return levels
	})
}

func NewSubagentWithThinkingLevels(run SubagentRun, supportedThinkingLevels func(SubagentModelProfile) []agent.ThinkingLevel) *Subagent {
	if supportedThinkingLevels == nil {
		return NewSubagent(run)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Subagent{
		run:                     run,
		supportedThinkingLevels: supportedThinkingLevels,
		ctx:                     ctx,
		cancel:                  cancel,
		jobs:                    make(map[string]*subagentJob),
		status:                  make(chan agent.SubagentStatus, 1),
		changes:                 make(chan struct{}),
	}
}

func (*Subagent) Definition() agent.ToolDefinition {
	return subagentToolDefinition
}

func (s *Subagent) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["tasks"].([]any)
	tasks := make([]subagentTask, 0, len(values))
	for _, value := range values {
		task, ok := value.(map[string]any)
		if !ok {
			continue
		}
		description, _ := task["description"].(string)
		prompt, _ := task["prompt"].(string)
		tasks = append(tasks, subagentTask{Description: description, Prompt: prompt})
	}
	modelProfile := defaultSubagentModelProfile
	if value, ok := snapshot.Arguments["model_profile"].(string); ok {
		modelProfile = SubagentModelProfile(value)
	}
	thinkingLevel, err := s.resolveThinkingLevel(modelProfile, nil)
	if err != nil {
		thinkingLevel = agent.ThinkingLow
	}
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
	thinkingLevel, err := s.resolveThinkingLevel(modelProfile, args.ThinkingLevel)
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
		s.signalChangeLocked()
		s.mu.Unlock()

		s.workers.Wait()
	})
	return nil
}

func validateSubagentTasks(tasks []subagentTask) error {
	if len(tasks) == 0 {
		return fmt.Errorf("at least one task is required")
	}
	if len(tasks) > maxSubagents {
		return fmt.Errorf("tasks must not exceed %d", maxSubagents)
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.Description) == "" {
			return fmt.Errorf("task descriptions must be nonempty")
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return fmt.Errorf("task prompts must be nonempty")
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

func (s *Subagent) resolveThinkingLevel(profile SubagentModelProfile, value *string) (agent.ThinkingLevel, error) {
	supported := s.supportedThinkingLevels(profile)
	level := agent.ThinkingLow
	if value == nil {
		level = agent.ClampThinkingLevel(level, supported)
	} else {
		level = agent.ThinkingLevel(*value)
	}

	switch level {
	case agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh:
	case agent.ThinkingXHigh, agent.ThinkingMax:
		return "", fmt.Errorf("thinking level %q is not available to subagents; use off, minimal, low, medium, or high", level)
	default:
		return "", fmt.Errorf("thinking level must be one of off, minimal, low, medium, or high")
	}
	if !slices.Contains(supported, level) {
		return "", fmt.Errorf("thinking level %q is not supported by the %s model", level, profile)
	}
	return level, nil
}

func (s *Subagent) start(tasks []subagentTask, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel) ([]*subagentJob, error) {
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
			order:         s.nextID,
			description:   task.Description,
			task:          task.Prompt,
			modelProfile:  modelProfile,
			thinkingLevel: thinkingLevel,
			ctx:           jobCtx,
			cancel:        cancel,
			state:         agent.SubagentRunning,
			started:       started,
		}
		s.jobs[job.id] = job
		jobs[index] = job
	}

	s.workers.Add(len(jobs))
	s.publishStatusLocked()
	s.signalChangeLocked()
	s.mu.Unlock()

	for _, job := range jobs {
		go s.runJob(job)
	}
	return jobs, nil
}

func (s *Subagent) runJob(job *subagentJob) {
	defer s.workers.Done()

	result, runErr := s.run(job.ctx, job.task, job.modelProfile, job.thinkingLevel, func(progress SubagentProgress) {
		s.mu.Lock()
		if !subagentStateTerminal(job.state) {
			if progress.Usage != (agent.Usage{}) {
				job.usage = progress.Usage
			}
			if progress.Generations > job.generations {
				job.generations = progress.Generations
			}
			if progress.Finalizing && job.state == agent.SubagentRunning {
				job.state = agent.SubagentFinalizing
			}
			s.publishStatusLocked()
			s.signalChangeLocked()
		}
		s.mu.Unlock()
	})
	cause := context.Cause(job.ctx)
	job.cancel(nil)
	if cause != nil {
		runErr = cause
	}

	s.mu.Lock()
	job.finished = time.Now()
	job.result = result
	job.err = runErr
	if result.Usage.TotalTokens > 0 {
		job.usage = result.Usage
	}
	switch {
	case errors.Is(runErr, errSubagentCanceled):
		job.state = agent.SubagentCanceled
		if s.jobs[job.id] == job {
			delete(s.jobs, job.id)
		}
	case errors.Is(runErr, errSubagentSessionClosed):
		job.state = agent.SubagentCanceled
	case runErr != nil:
		job.state = agent.SubagentFailed
	default:
		job.state = agent.SubagentComplete
	}
	s.publishStatusLocked()
	s.signalChangeLocked()
	s.mu.Unlock()
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
		if subagentStateTerminal(job.state) || job.state == agent.SubagentCanceling {
			continue
		}
		job.state = agent.SubagentCanceling
		job.cancel(cause)
		canceled = append(canceled, job.id)
	}
	if len(canceled) > 0 {
		s.publishStatusLocked()
		s.signalChangeLocked()
	}
	s.mu.Unlock()
	return canceled
}

func (s *Subagent) snapshotJobs(jobs []*subagentJob) ([]subagentJobSnapshot, bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots := make([]subagentJobSnapshot, len(jobs))
	complete := true
	for index, job := range jobs {
		snapshots[index] = subagentJobSnapshot{
			id:            job.id,
			modelProfile:  job.modelProfile,
			thinkingLevel: job.thinkingLevel,
			result:        subagentResult{text: job.result.Text, err: job.err},
		}
		if !subagentStateTerminal(job.state) {
			complete = false
		}
	}
	return snapshots, complete, s.changes
}

func (s *Subagent) consume(jobs []*subagentJob) {
	s.mu.Lock()
	for _, job := range jobs {
		if s.jobs[job.id] == job {
			delete(s.jobs, job.id)
		}
	}
	s.publishStatusLocked()
	s.signalChangeLocked()
	s.mu.Unlock()
}

func (s *Subagent) publishStatusLocked() {
	status := agent.SubagentStatus{}
	jobs := make([]*subagentJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
		switch job.state {
		case agent.SubagentFinalizing:
			status.Finalizing++
		case agent.SubagentRunning, agent.SubagentCanceling:
			status.Running++
		default:
			status.Completed++
		}
	}
	slices.SortFunc(jobs, func(left, right *subagentJob) int {
		switch {
		case left.order < right.order:
			return -1
		case left.order > right.order:
			return 1
		default:
			return 0
		}
	})
	status.Jobs = make([]agent.SubagentJobStatus, len(jobs))
	for index, job := range jobs {
		status.Jobs[index] = agent.SubagentJobStatus{
			ID:              job.id,
			Task:            job.description,
			ModelProfile:    string(job.modelProfile),
			ThinkingLevel:   job.thinkingLevel,
			State:           job.state,
			Started:         job.started,
			Finished:        job.finished,
			Usage:           job.usage,
			Generations:     job.generations,
			GenerationLimit: subagentFinalizeAfterGenerations,
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

func (s *Subagent) signalChangeLocked() {
	close(s.changes)
	s.changes = make(chan struct{})
}

func subagentStateTerminal(state agent.SubagentState) bool {
	switch state {
	case agent.SubagentComplete, agent.SubagentFailed, agent.SubagentCanceled:
		return true
	default:
		return false
	}
}

func formatSubagentLaunches(jobs []*subagentJob, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Started subagents (model: %s, thinking: %s):", modelProfile, thinkingLevel)
	for _, job := range jobs {
		fmt.Fprintf(&output, "\n- %s: %s", job.id, strings.TrimSpace(job.description))
	}
	return output.String()
}

func subagentLaunchPresentation(tasks []subagentTask, ids []string, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel) agent.ToolPresentation {
	presentation := agent.ToolPresentation{
		Title:     subagentToolName,
		Arguments: fmt.Sprintf("(%s, %s)", modelProfile, thinkingLevel),
		Markdown:  true,
	}
	if len(tasks) == 0 {
		return presentation
	}

	state := "Starting"
	if len(ids) > 0 {
		state = "Started"
	}
	presentation.Lines = []string{fmt.Sprintf("%s %d subagent(s).", state, len(tasks))}
	return presentation
}
