package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
)

const (
	maxInputBytes = 1024 * 1024
)

var (
	ErrInterrupted  = errors.New("terminal: interrupted")
	ErrNotTerminal  = errors.New("terminal: interactive mode requires terminal input and output")
	errInputTooLong = errors.New("terminal: input is too long")
	errInvalidInput = errors.New("terminal: input must be valid UTF-8 text without NUL")
	errOutput       = errors.New("terminal: write output")
)

type EventStream interface {
	Emit(agent.Event) error
	Snapshot() (Checkpoint, error)
}

type Operations struct {
	RunTurn func(context.Context, []agent.ContentPart, EventStream) error
	Compact func(context.Context, EventStream) error
}

type Controls struct {
	Steer            func(string) bool
	ClearSteering    func() []string
	SetGoal          func(string) error
	Goal             func() (agent.GoalState, bool)
	ClearGoal        func()
	SetThinkingLevel func(agent.ThinkingLevel) error
	SetFastMode      func(bool) error
}

type SessionSummary struct {
	ID          string
	Description string
	UpdatedAt   time.Time
	Active      bool
}

type RunAction uint8

const (
	RunExit RunAction = iota
	RunNewSession
	RunResumeSession
)

type RunOutcome struct {
	Action    RunAction
	SessionID string
}

type PermissionDecision uint8

const (
	PermissionDenyOnce PermissionDecision = iota
	PermissionAllowOnce
	PermissionAllowSession
)

type PermissionRequest struct {
	Title        string
	Subject      string
	Description  string
	Detail       string
	DetailPrefix string
	Notice       string
	Response     chan<- PermissionDecision
}

type Config struct {
	Model              string
	WorkingDirectory   string
	ThinkingLevel      agent.ThinkingLevel
	ThinkingLevels     []agent.ThinkingLevel
	FastMode           bool
	FastModeAvailable  bool
	ContextWindow      int64
	Skills             []skill.Skill
	Warnings           []string
	InitialCheckpoint  *Checkpoint
	SessionID          string
	PreviousTurnActive bool
}

type Sessions struct {
	List func(context.Context) ([]SessionSummary, []string, error)
}

type StateChanges struct {
	Notify func(Checkpoint, bool) error
}

type Events struct {
	Interrupts         <-chan os.Signal
	SubagentUpdates    <-chan subagent.Status
	PermissionRequests <-chan PermissionRequest
}

type ProviderUsage struct {
	Windows []UsageWindow
}

type UsageWindow struct {
	Duration    time.Duration
	UsedPercent int
	ResetsAt    time.Time
}

type Services struct {
	LoadUsage          func(context.Context) (ProviderUsage, error)
	ReadClipboardImage func(context.Context) (agent.Image, error)
}

type Options struct {
	Input        io.Reader
	Output       io.Writer
	Config       Config
	Operations   Operations
	Controls     Controls
	Sessions     Sessions
	StateChanges StateChanges
	Events       Events
	Services     Services
}

type fileDescriptor interface {
	Fd() uintptr
}

func descriptor(value any) (int, bool) {
	provider, ok := value.(fileDescriptor)
	if !ok {
		return 0, false
	}

	return int(provider.Fd()), true
}

func writeOutput(writer io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(writer, format, arguments...); err != nil {
		return fmt.Errorf("%w: %v", errOutput, err)
	}
	return nil
}
