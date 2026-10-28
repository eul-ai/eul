package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/terminal/keys"
)

const maxInputBytes = keys.MaxPasteBytes

var (
	ErrInterrupted = errors.New("terminal: interrupted")
	errNotTerminal = errors.New("terminal: interactive mode requires terminal input and output")
	errOutput      = errors.New("terminal: write output")
)

type Session interface {
	Updates() <-chan session.Update
	Send([]agent.ContentPart) error
	Compact() error
	Interrupt() [][]agent.ContentPart
	TakeQueued() [][]agent.ContentPart
	Queued() [][]agent.ContentPart
	Running() bool
	Sync([]session.Entry) ([]session.Entry, bool)
	Append(session.EntryKind, string)
	SetDraft(session.Draft)
	SaveDraft(session.Draft) error
	Permissions() []*session.PermissionRequest
	ResolvePermission(session.PermissionDecision)
}

type Controls struct {
	SetGoal          func(string) error
	Goal             func() (agent.GoalState, bool)
	ClearGoal        func()
	SetThinkingLevel func(agent.ThinkingLevel) error
	SetFastMode      func(bool)
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

type Config struct {
	Model             string
	WorkingDirectory  string
	ThinkingLevel     agent.ThinkingLevel
	ThinkingLevels    []agent.ThinkingLevel
	FastMode          bool
	FastModeAvailable bool
	ContextWindow     int64
	ContextTokens     int64
	Skills            []skill.Skill
	Draft             session.Draft
	SessionID         string
	InitialPrompt     string
}

type Sessions struct {
	List func(context.Context) ([]session.Summary, error)
}

type MessageHistory struct {
	Entries []string
	Append  func(string) error
}

type Events struct {
	Interrupts <-chan os.Signal
}

type Services struct {
	LoadUsage func(context.Context) (backend.AccountUsage, error)
}

type Options struct {
	Config         Config
	Controls       Controls
	Session        Session
	Sessions       Sessions
	MessageHistory MessageHistory
	Events         Events
	Services       Services
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
