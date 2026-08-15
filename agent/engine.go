package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/eul-ai/eul/skill"
)

var errEngineBusy = errors.New("agent: engine is busy")

type Options struct {
	Model                  string
	WorkingDirectory       string
	ProjectInstructions    string
	Skills                 []skill.Skill
	Checkpointing          bool
	Inbox                  InboxSource
	AdditionalInstructions func() string
	Settings               *Settings
}

type RunResult struct {
	Text  string
	Usage Usage
}

type Engine struct {
	mu                     sync.Mutex
	provider               Provider
	tools                  Toolbox
	sessionID              string
	model                  string
	settings               *Settings
	instructions           string
	conversation           conversationState
	continuations          continuationArbiter
	skills                 []skill.Skill
	checkpointing          bool
	inbox                  InboxSource
	additionalInstructions func() string
}

func New(provider Provider, tools Toolbox, options Options) *Engine {
	settings := options.Settings
	if settings == nil {
		settings = NewSettings(DefaultThinkingLevel, false)
	}
	skills := append([]skill.Skill(nil), options.Skills...)
	instructions := buildSystemPrompt(tools.Definitions(), options.WorkingDirectory, options.ProjectInstructions, options.Skills)

	return &Engine{
		provider:               provider,
		tools:                  tools,
		model:                  options.Model,
		settings:               settings,
		instructions:           instructions,
		skills:                 skills,
		checkpointing:          options.Checkpointing,
		inbox:                  options.Inbox,
		additionalInstructions: options.AdditionalInstructions,
	}
}

func (e *Engine) Run(ctx context.Context, userText string, sink EventSink) (RunResult, error) {
	return e.run(ctx, []ContentPart{{Kind: ContentPartText, Text: userText}}, sink)
}

func (e *Engine) RunContent(ctx context.Context, content []ContentPart, sink EventSink) (RunResult, error) {
	return e.run(ctx, content, sink)
}

func (e *Engine) run(ctx context.Context, content []ContentPart, sink EventSink) (RunResult, error) {
	if !e.mu.TryLock() {
		return RunResult{}, errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	content, err := e.expandSkillContent(content)
	if err != nil {
		return RunResult{}, err
	}

	e.beginContinuations()
	defer e.endContinuations()

	current := e.conversation.clone()
	current.inputs = append(current.inputs, userInput(content))
	return (&engineTurn{
		engine:  e,
		ctx:     ctx,
		sink:    sink,
		current: current,
	}).run()
}

type conversationState struct {
	state  []byte
	usage  Usage
	inputs []Input
}

func (current conversationState) clone() conversationState {
	current.state = append([]byte(nil), current.state...)
	current.inputs = cloneInputs(current.inputs)
	return current
}

func (current conversationState) checkpoint(engine *Engine) {
	engine.conversation = current.clone()
}

func (e *Engine) Compact(ctx context.Context, sink EventSink) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	compactor, canCompact := e.provider.(Compactor)
	if !canCompact {
		return errors.New("agent: context compaction is unavailable")
	}

	current := e.conversation.clone()
	if len(current.state) == 0 && len(current.inputs) == 0 {
		return errors.New("agent: no context to compact")
	}

	_, current, _, err := e.compactRequest(ctx, sink, compactor, e.request(current), current)
	current.checkpoint(e)
	return err
}

func (e *Engine) SetSessionID(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.sessionID = sessionID
}

func (e *Engine) SetThinkingLevel(level ThinkingLevel) error {
	return e.settings.SetThinkingLevel(level)
}

func (e *Engine) SetFastMode(enabled bool) {
	e.settings.SetFastMode(enabled)
}

func (e *Engine) currentThinkingLevel() ThinkingLevel {
	level, _ := e.currentSettings()
	return level
}

func (e *Engine) currentSettings() (ThinkingLevel, bool) {
	return e.settings.Snapshot()
}

func (e *Engine) Reset() error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	e.conversation = conversationState{}
	e.continuations.reset()
	return nil
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

func (e *Engine) beginContinuations() {
	e.continuations.beginRun()
}

func (e *Engine) endContinuations() {
	e.continuations.endRun()
}

func deliverContinuation(current *conversationState, next pendingContinuation, sink EventSink) error {
	current.inputs = append(current.inputs, userInput(next.content))
	event := Event{Kind: EventSteering, Content: cloneContentParts(next.content)}
	if next.kind == continuationGoal {
		event.Kind = EventGoalContinuation
		event.Text = NewUserInput(next.content...).PlainText()
		event.Content = nil
	}
	if err := emit(sink, event); err != nil {
		current.inputs = current.inputs[:len(current.inputs)-1]
		return err
	}
	return nil
}

func userInput(content []ContentPart) Input {
	return NewUserInput(content...)
}

func toolResultInput(result ToolResult) Input {
	return NewToolResultInput(result)
}

func emit(sink EventSink, event Event) error {
	return sink(event)
}

func addUsage(total *Usage, usage Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
}
