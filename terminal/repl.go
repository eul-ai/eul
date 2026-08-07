package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"yaah/agent"
)

const (
	maxInputBytes                   = 1024 * 1024
	maxToolPresentationSummaryBytes = 500
)

var (
	ErrInterrupted  = errors.New("terminal: interrupted")
	ErrNotTerminal  = errors.New("terminal: interactive mode requires terminal input and output; provide a prompt for one-shot mode")
	errInputTooLong = errors.New("terminal: input is too long")
	errInvalidInput = errors.New("terminal: input must be valid UTF-8 text without NUL")
	errOutput       = errors.New("terminal: write output")
)

type Engine interface {
	Run(context.Context, string, agent.EventSink) (agent.RunResult, error)
	Reset()
}

type Options struct {
	Input            io.Reader
	Output           io.Writer
	ErrorOutput      io.Writer
	Model            string
	WorkingDirectory string
	ThinkingLevel    agent.ThinkingLevel
	ThinkingLevels   []agent.ThinkingLevel
	ContextWindow    int64
	Interrupts       <-chan os.Signal
	SetThinkingLevel func(agent.ThinkingLevel) error
	LoadUsage        func(context.Context) (agent.ProviderUsage, error)
}

type fileDescriptor interface {
	Fd() uintptr
}

func IsTerminal(value any) bool {
	fd, ok := descriptor(value)
	return ok && term.IsTerminal(fd)
}

func descriptor(value any) (int, bool) {
	provider, ok := value.(fileDescriptor)
	if !ok {
		return 0, false
	}

	return int(provider.Fd()), true
}

func RunOneShot(ctx context.Context, engine Engine, prompt string, options Options) error {
	runErr, interrupted := runTurn(ctx, engine, prompt, options)
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}

	if interrupted {
		return ErrInterrupted
	}
	return runErr
}

func runTurn(parent context.Context, engine Engine, prompt string, options Options) (error, bool) {
	turnContext, cancel := context.WithCancel(parent)
	defer cancel()

	renderer := eventRenderer{output: options.Output, errorOutput: options.ErrorOutput}
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(turnContext, prompt, renderer.render)
		done <- err
	}()

	interrupted := false
	parentCanceled := false
	interrupts := options.Interrupts
	parentDone := parent.Done()
	finish := func(runErr error) (error, bool) {
		if err := renderer.finish(); err != nil {
			return err, interrupted
		}
		if parentCanceled {
			return parent.Err(), false
		}
		return runErr, interrupted
	}

	for {
		select {
		case runErr := <-done:
			return finish(runErr)
		case _, ok := <-interrupts:
			if !ok {
				interrupts = nil
				continue
			}
			select {
			case runErr := <-done:
				return finish(runErr)
			default:
			}

			if interrupted {
				continue
			}
			interrupted = true
			cancel()
		case <-parentDone:
			parentDone = nil
			parentCanceled = true
			cancel()
		}
	}
}

type eventRenderer struct {
	output         io.Writer
	errorOutput    io.Writer
	assistantOpen  bool
	reasoningOpen  bool
	executingTools map[string]struct{}
}

func (r *eventRenderer) render(event agent.Event) error {
	switch event.Kind {
	case agent.EventAssistantText:
		if err := r.finishReasoning(); err != nil {
			return err
		}
		text := sanitizeAssistantText(event.Text)
		if err := writeOutput(r.output, "%s", text); err != nil {
			return err
		}
		if text != "" {
			r.assistantOpen = !strings.HasSuffix(text, "\n")
		}
	case agent.EventAssistantReasoning:
		if err := r.finishAssistant(); err != nil {
			return err
		}
		text := sanitizeAssistantText(event.Text)
		if err := writeOutput(r.errorOutput, "%s", text); err != nil {
			return err
		}
		if text != "" {
			r.reasoningOpen = !strings.HasSuffix(text, "\n")
		}
	case agent.EventCompactionStart:
		if err := r.finish(); err != nil {
			return err
		}
		if err := writeOutput(r.errorOutput, "[context] compacting conversation\n"); err != nil {
			return err
		}
	case agent.EventToolExecute:
		if err := r.finish(); err != nil {
			return err
		}
		if err := writeOutput(r.errorOutput, "[tool] %s\n", summarizeToolExecution(event.Call, event.Presentation)); err != nil {
			return err
		}
		if r.executingTools == nil {
			r.executingTools = make(map[string]struct{})
		}
		r.executingTools[event.Call.ID] = struct{}{}
	case agent.EventToolEnd:
		if _, exists := r.executingTools[event.Call.ID]; !exists {
			return nil
		}
		delete(r.executingTools, event.Call.ID)
		if err := writeOutput(r.errorOutput, "[tool] %s\n", summarizeToolEnd(event.Call, event.Presentation, event.Result)); err != nil {
			return err
		}
	}
	return nil
}

func (r *eventRenderer) finish() error {
	if err := r.finishAssistant(); err != nil {
		return err
	}
	return r.finishReasoning()
}

func (r *eventRenderer) finishAssistant() error {
	if !r.assistantOpen {
		return nil
	}

	r.assistantOpen = false
	return writeOutput(r.output, "\n")
}

func (r *eventRenderer) finishReasoning() error {
	if !r.reasoningOpen {
		return nil
	}

	r.reasoningOpen = false
	return writeOutput(r.errorOutput, "\n")
}

func summarizeToolExecution(call agent.ToolCall, presentation agent.ToolPresentation) string {
	return diagnostic(toolHeading(call, presentation), 200)
}

func summarizeToolEnd(call agent.ToolCall, presentation agent.ToolPresentation, result agent.ToolResult) string {
	if call.Name == "" {
		call.Name = result.Tool
	}
	return diagnostic(toolHeading(call, presentation)+" — "+toolResultOutcome(result, presentation), 200)
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
