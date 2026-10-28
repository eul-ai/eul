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
	SessionID     string
	Checkpoint    Checkpoint
	Checkpointing bool
	Inbox         Inbox
	Settings      *Settings
	Goal          *Goal
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
	goal := options.Goal
	if goal == nil {
		goal = &Goal{}
	}
	if saved := options.Checkpoint.Goal; saved != nil {
		restored := *saved
		goal.restore(&restored)
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
		provider:     provider,
		tools:        byName,
		definitions:  definitions,
		sessionID:    options.SessionID,
		model:        options.Model,
		settings:     settings,
		instructions: options.Instructions,
		conversation: conversationState{
			state:  options.Checkpoint.State,
			usage:  options.Checkpoint.ContextUsage,
			inputs: options.Checkpoint.PendingInputs,
		},
		continuations: continuationArbiter{goal: goal},
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

func (e *Engine) Steer(content []ContentPart) bool {
	return e.continuations.steer(content)
}

func (e *Engine) ClearSteering() [][]ContentPart {
	return e.continuations.clearSteering()
}
