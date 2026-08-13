package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const (
	maxInputBytes                   = 1024 * 1024
	maxToolPresentationSummaryBytes = 500
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

type Options struct {
	Input              io.Reader
	Output             io.Writer
	Model              string
	WorkingDirectory   string
	ThinkingLevel      agent.ThinkingLevel
	ThinkingLevels     []agent.ThinkingLevel
	FastMode           bool
	FastModeAvailable  bool
	ContextWindow      int64
	Skills             []agent.Skill
	Warnings           []string
	Interrupts         <-chan os.Signal
	SetThinkingLevel   func(agent.ThinkingLevel) error
	SetFastMode        func(bool) error
	LoadUsage          func(context.Context) (agent.ProviderUsage, error)
	SubagentUpdates    <-chan SubagentStatus
	PermissionRequests <-chan PermissionRequest
	InitialCheckpoint  *Checkpoint
	SessionID          string
	PreviousTurnActive bool
	SaveCheckpoint     func(agent.Checkpoint, Checkpoint, bool) error
	ListSessions       func(context.Context) ([]SessionSummary, []string, error)
	ReadClipboardImage func(context.Context) (agent.Image, error)
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

func toolTitle(call agent.ToolCall, presentation agent.ToolPresentation) string {
	if title := strings.TrimSpace(presentation.Title); title != "" {
		return title
	}
	if call.Name != "" {
		return call.Name
	}
	return "tool"
}

func toolHeading(call agent.ToolCall, presentation agent.ToolPresentation) string {
	title := toolTitle(call, presentation)
	if arguments := strings.TrimSpace(presentation.Arguments); arguments != "" {
		title += " " + arguments
	}
	return title
}

func toolActivityDetail(call agent.ToolCall, presentation agent.ToolPresentation) string {
	if call.Name == "bash" || toolTitle(call, presentation) == "bash" {
		return "bash"
	}
	return diagnostic(toolHeading(call, presentation), maxToolPresentationSummaryBytes)
}

func toolResultOutcome(result agent.ToolResult, presentation agent.ToolPresentation) string {
	if presentation.Outcome != "" {
		return presentation.Outcome
	}
	if !result.IsError {
		return "ok"
	}
	detail := result.Output
	if newline := strings.IndexByte(detail, '\n'); newline >= 0 {
		detail = detail[:newline]
	}
	if detail == "" {
		return "error"
	}
	return "error: " + detail
}

func sanitizeToolPresentation(call agent.ToolCall, presentation agent.ToolPresentation) agent.ToolPresentation {
	presentation = presentation.Clone()
	presentation.Title = diagnostic(toolTitle(call, presentation), maxToolPresentationSummaryBytes)
	presentation.Arguments = diagnostic(presentation.Arguments, maxToolPresentationSummaryBytes)
	presentation.Outcome = diagnostic(presentation.Outcome, maxToolPresentationSummaryBytes)
	presentation.TailLines = max(0, presentation.TailLines)
	presentation.Elapsed = max(0, presentation.Elapsed)
	presentation.Timeout = max(0, presentation.Timeout)
	for index := range presentation.Lines {
		presentation.Lines[index] = sanitizeAssistantText(presentation.Lines[index])
	}
	for index := range presentation.Diff {
		presentation.Diff[index].Text = sanitizeAssistantText(presentation.Diff[index].Text)
	}
	return presentation
}

func diagnostic(value string, maximum int) string {
	return singleLine(value, maximum)
}

func sanitizeAssistantText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
}

func singleLine(value string, maximum int) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")

	if len(value) <= maximum {
		return value
	}
	end := maximum - 3
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}

func writeOutput(writer io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(writer, format, arguments...); err != nil {
		return fmt.Errorf("%w: %v", errOutput, err)
	}
	return nil
}
