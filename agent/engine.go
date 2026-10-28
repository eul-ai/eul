package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
)

var errEngineBusy = errors.New("agent: engine is busy")

type Options struct {
	Model         string
	Instructions  string
	Checkpointing bool
	Inbox         Inbox
	Settings      *Settings
}

type RunResult struct {
	Text string
}

type Engine struct {
	mu            sync.Mutex
	provider      Provider
	tools         map[string]Tool
	definitions   []ToolDefinition
	sessionID     string
	model         string
	settings      *Settings
	instructions  string
	conversation  conversationState
	continuations continuationArbiter
	checkpointing bool
	inbox         Inbox
}

func New(provider Provider, tools []Tool, options Options) *Engine {
	settings := options.Settings
	if settings == nil {
		settings = NewSettings(DefaultThinkingLevel, false)
	}

	byName := make(map[string]Tool, len(tools))
	definitions := make([]ToolDefinition, len(tools))
	for index, tool := range tools {
		definitions[index] = tool.Definition()
		byName[definitions[index].Name] = tool
	}
	slices.SortFunc(definitions, func(left, right ToolDefinition) int {
		return strings.Compare(left.Name, right.Name)
	})

	return &Engine{
		provider:      provider,
		tools:         byName,
		definitions:   definitions,
		model:         options.Model,
		settings:      settings,
		instructions:  options.Instructions,
		checkpointing: options.Checkpointing,
		inbox:         options.Inbox,
	}
}

func (e *Engine) Run(ctx context.Context, content []ContentPart, sink EventSink) (RunResult, error) {
	if !e.mu.TryLock() {
		return RunResult{}, errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	e.continuations.setRunning(true)
	defer e.continuations.setRunning(false)

	t := &turn{engine: e, ctx: ctx, sink: sink, current: e.conversation}
	t.current.inputs = append(slices.Clip(t.current.inputs), NewUserInput(content...))
	defer func() { e.conversation = t.current }()
	return t.run()
}

func (e *Engine) Compact(ctx context.Context, sink EventSink) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	if len(e.conversation.state) == 0 && len(e.conversation.inputs) == 0 {
		return errors.New("agent: no context to compact")
	}

	t := &turn{engine: e, ctx: ctx, sink: sink, current: e.conversation}
	defer func() { e.conversation = t.current }()
	return t.compact()
}

func (e *Engine) SetSessionID(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.sessionID = sessionID
}

func (e *Engine) Steer(content []ContentPart) bool {
	return e.continuations.steer(content)
}

func (e *Engine) ClearSteering() [][]ContentPart {
	return e.continuations.clearSteering()
}

func (e *Engine) SetGoal(objective string) error {
	return e.continuations.setGoal(objective)
}

func (e *Engine) Goal() (GoalState, bool) {
	return e.continuations.getGoal()
}

func (e *Engine) ClearGoal() {
	e.continuations.clearGoal()
}

func (e *Engine) CompleteGoal() error {
	return e.continuations.completeGoal()
}
