package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"yaah/agent"
)

const maxInputBytes = 1024 * 1024

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
	NeedsReset() bool
}

type Options struct {
	Input         io.Reader
	Output        io.Writer
	ErrorOutput   io.Writer
	Model         string
	Effort        string
	ContextWindow int64
	Interrupts    <-chan os.Signal
	SetEffort     func(string) error
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
		resetIfNeeded(engine)
		return contextErr
	}

	if interrupted {
		resetIfNeeded(engine)
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

func resetIfNeeded(engine Engine) bool {
	if !engine.NeedsReset() {
		return false
	}

	engine.Reset()
	return true
}

type eventRenderer struct {
	output        io.Writer
	errorOutput   io.Writer
	assistantOpen bool
	reasoningOpen bool
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
	case agent.EventToolStart:
		if err := r.finish(); err != nil {
			return err
		}
		if err := writeOutput(r.errorOutput, "[tool] %s\n", summarizeCall(event.Call)); err != nil {
			return err
		}
	case agent.EventToolEnd:
		if err := writeOutput(r.errorOutput, "[tool] %s\n", summarizeResult(event.Result)); err != nil {
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

func summarizeCall(call agent.ToolCall) string {
	argumentName := ""
	switch call.Name {
	case "read", "write", "edit":
		argumentName = "path"
	case "bash":
		argumentName = "command"
	}
	if argumentName == "" {
		return diagnostic(call.Name, 160)
	}

	var arguments map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &arguments) != nil {
		return diagnostic(call.Name, 160)
	}

	var value string
	if json.Unmarshal(arguments[argumentName], &value) != nil || value == "" {
		return diagnostic(call.Name, 160)
	}
	return diagnostic(call.Name+" "+displayArgument(value), 160)
}

func summarizeResult(result agent.ToolResult) string {
	if result.Tool == "bash" {
		if status := bashStatus(result.Output); status != "" {
			return diagnostic("bash — "+status, 200)
		}
	}

	if !result.IsError {
		return diagnostic(result.Tool+" — ok", 200)
	}
	detail := result.Output
	if newline := strings.IndexByte(detail, '\n'); newline >= 0 {
		detail = detail[:newline]
	}
	return diagnostic(result.Tool+" — error: "+detail, 200)
}

func bashStatus(output string) string {
	index := strings.LastIndex(output, "[exit status:")
	if index < 0 {
		return ""
	}

	status := output[index+1:]
	if end := strings.IndexByte(status, ']'); end >= 0 {
		status = status[:end]
	}
	return status
}

func displayArgument(value string) string {
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return strconv.Quote(value)
	}
	return value
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
