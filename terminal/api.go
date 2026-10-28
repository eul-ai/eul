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
	"github.com/eul-ai/eul/terminal/keys"
)

const maxInputBytes = keys.MaxPasteBytes

var (
	ErrInterrupted = errors.New("terminal: interrupted")
	errNotTerminal = errors.New("terminal: interactive mode requires terminal input and output")
	errOutput      = errors.New("terminal: write output")
)

// Session is the conversation the terminal shows. *session.Session
// implements it.
type Session interface {
	Info() session.Info
	Settings() session.Settings
	SetThinkingLevel(agent.ThinkingLevel) error
	SetFastMode(bool)
	Goal() (agent.GoalState, bool)
	SetGoal(string) error
	ClearGoal()

	Updates() <-chan session.Update
	Send([]agent.ContentPart) error
	Compact() error
	Interrupt() [][]agent.ContentPart
	TakeQueued() [][]agent.ContentPart
	Queued() [][]agent.ContentPart
	Running() bool
	Permissions() []*session.PermissionRequest
	ResolvePermission(session.PermissionDecision)

	Sync([]session.Entry) ([]session.Entry, bool)
	Append(session.EntryKind, string)
	SetDraft(session.Draft)
	SaveDraft(session.Draft) error

	Remember(string) error
	Sessions() ([]session.Summary, error)
	Usage(context.Context) (backend.AccountUsage, error)
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

type Options struct {
	Session    Session
	Interrupts <-chan os.Signal
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
