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
)

const (
	maxInputBytes = 1024 * 1024
)

var (
	ErrInterrupted           = errors.New("terminal: interrupted")
	ErrNotTerminal           = errors.New("terminal: interactive mode requires terminal input and output")
	errInputTooLong          = errors.New("terminal: input is too long")
	errInvalidInput          = errors.New("terminal: input must be valid UTF-8 text without NUL")
	errOutput                = errors.New("terminal: write output")
	errCheckpointUnavailable = errors.New("terminal: engine checkpointing is unavailable")
)

type Engine interface {
	Run(context.Context, string, agent.EventSink) (agent.RunResult, error)
	RunContent(context.Context, []agent.ContentPart, agent.EventSink) (agent.RunResult, error)
	Compact(context.Context, agent.EventSink) error
	Steer(string) bool
	ClearSteering() []string
	SetGoal(string) error
	Goal() (agent.GoalState, bool)
	ClearGoal()
}

type checkpointEngine interface {
	Checkpoint() (agent.Checkpoint, error)
}

func validateCheckpointCapability(engine Engine, required bool) error {
	if !required {
		return nil
	}
	if _, ok := engine.(checkpointEngine); !ok {
		return errCheckpointUnavailable
	}
	return nil
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

type PermissionRequest struct {
	Title        string
	Subject      string
	Description  string
	Detail       string
	DetailPrefix string
	Notice       string
	Response     chan<- bool
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

type Commands interface {
	SetThinkingLevel(agent.ThinkingLevel) error
	SetFastMode(bool) error
}

type Persistence interface {
	SaveCheckpoint(agent.Checkpoint, Checkpoint, bool) error
	ListSessions(context.Context) ([]SessionSummary, []string, error)
}

type commandCapabilities interface {
	CanSetThinkingLevel() bool
	CanSetFastMode() bool
}

type persistenceCapabilities interface {
	CanSaveCheckpoint() bool
	CanListSessions() bool
}

func canSetThinkingLevel(commands Commands) bool {
	capabilities, ok := commands.(commandCapabilities)
	return commands != nil && (!ok || capabilities.CanSetThinkingLevel())
}

func canSetFastMode(commands Commands) bool {
	capabilities, ok := commands.(commandCapabilities)
	return commands != nil && (!ok || capabilities.CanSetFastMode())
}

func canSaveCheckpoint(persistence Persistence) bool {
	capabilities, ok := persistence.(persistenceCapabilities)
	return persistence != nil && (!ok || capabilities.CanSaveCheckpoint())
}

func canListSessions(persistence Persistence) bool {
	capabilities, ok := persistence.(persistenceCapabilities)
	return persistence != nil && (!ok || capabilities.CanListSessions())
}

type Events struct {
	Interrupts         <-chan os.Signal
	SubagentUpdates    <-chan SubagentStatus
	PermissionRequests <-chan PermissionRequest
}

type Services struct {
	LoadUsage          func(context.Context) (ProviderUsage, error)
	ReadClipboardImage func(context.Context) (agent.Image, error)
}

type Options struct {
	Input       io.Reader
	Output      io.Writer
	Config      Config
	Commands    Commands
	Persistence Persistence
	Events      Events
	Services    Services
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
