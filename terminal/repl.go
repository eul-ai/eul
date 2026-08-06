package terminal

import (
	"bufio"
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

	"yaah/agent"
)

const maxInputBytes = 1024 * 1024

var (
	ErrInterrupted  = errors.New("terminal: interrupted")
	ErrInputTooLong = errors.New("terminal: input line is too long")
	ErrInvalidInput = errors.New("terminal: input must be valid UTF-8 text without NUL")
	ErrOutput       = errors.New("terminal: write output")
)

// Engine is the conversation runner consumed by the terminal.
type Engine interface {
	Run(context.Context, string, agent.EventSink) (agent.RunResult, error)
	Reset()
	NeedsReset() bool
}

// Options configures terminal input, output, display metadata, and interrupts.
type Options struct {
	Input       io.Reader
	Output      io.Writer
	ErrorOutput io.Writer
	Model       string
	CWD         string
	Interrupts  <-chan os.Signal
}

// Run starts the line-oriented interactive REPL.
func Run(ctx context.Context, engine Engine, options Options) error {
	header := singleLine(fmt.Sprintf("yaah · openai/%s · %s", options.Model, options.CWD), 500)
	if err := writeOutput(options.ErrorOutput, "%s\n", header); err != nil {
		return err
	}

	inputContext, cancelInput := context.WithCancel(ctx)
	defer cancelInput()
	inputEvents, inputRequests := readInput(inputContext, options.Input, maxInputBytes)
	for {
		if err := writeOutput(options.ErrorOutput, "> "); err != nil {
			return err
		}
		select {
		case inputRequests <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-options.Interrupts:
			if !ok {
				options.Interrupts = nil
				continue
			}
			if err := writeOutput(options.ErrorOutput, "\n"); err != nil {
				return err
			}
			return ErrInterrupted
		case event, ok := <-inputEvents:
			if !ok {
				if err := writeOutput(options.ErrorOutput, "\n"); err != nil {
					return err
				}
				return nil
			}
			if errors.Is(event.err, io.EOF) {
				if err := writeOutput(options.ErrorOutput, "\n"); err != nil {
					return err
				}
				return nil
			}
			if errors.Is(event.err, ErrInputTooLong) {
				if err := writeOutput(options.ErrorOutput, "error: input line exceeds %d bytes\n", maxInputBytes); err != nil {
					return err
				}
				continue
			}
			if errors.Is(event.err, ErrInvalidInput) {
				if err := writeOutput(options.ErrorOutput, "error: input must be valid UTF-8 text without NUL\n"); err != nil {
					return err
				}
				continue
			}
			if event.err != nil {
				return fmt.Errorf("terminal: read input: %w", event.err)
			}

			trimmed := strings.TrimSpace(event.line)
			if trimmed == "" {
				continue
			}
			switch trimmed {
			case "/help":
				if err := writeOutput(options.Output, "Commands:\n  /help   show this help\n  /clear  discard conversation state\n  /exit   exit yaah\n"); err != nil {
					return err
				}
				continue
			case "/clear":
				engine.Reset()
				if err := writeOutput(options.ErrorOutput, "[conversation cleared]\n"); err != nil {
					return err
				}
				continue
			case "/exit":
				return nil
			}
			if strings.HasPrefix(trimmed, "/") {
				if err := writeOutput(options.ErrorOutput, "error: unknown command %s\n", diagnostic(trimmed, 120)); err != nil {
					return err
				}
				continue
			}

			runErr, interrupted := runTurn(ctx, engine, event.line, options)
			if contextErr := ctx.Err(); contextErr != nil {
				resetIfNeeded(engine)
				return contextErr
			}
			if errors.Is(runErr, ErrOutput) {
				return runErr
			}
			if interrupted {
				cleared := resetIfNeeded(engine)
				message := "[interrupted]"
				if cleared {
					message = "[interrupted; conversation cleared after incomplete tool turn]"
				}
				if err := writeOutput(options.ErrorOutput, "%s\n", message); err != nil {
					return err
				}
				continue
			}
			if runErr != nil {
				if err := writeOutput(options.ErrorOutput, "error: %s\n", diagnostic(runErr.Error(), 500)); err != nil {
					return err
				}
				if resetIfNeeded(engine) {
					if err := writeOutput(options.ErrorOutput, "[conversation cleared after incomplete tool turn]\n"); err != nil {
						return err
					}
				}
			}
		}
	}
}

// RunOneShot runs one prompt without the interactive header or prompt marker.
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
			if !interrupted {
				interrupted = true
				cancel()
			}
		case <-parentDone:
			parentDone = nil
			if !parentCanceled {
				parentCanceled = true
				cancel()
			}
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
		return fmt.Errorf("%w: %v", ErrOutput, err)
	}
	return nil
}

type inputEvent struct {
	line string
	err  error
}

func readInput(ctx context.Context, input io.Reader, maximum int) (<-chan inputEvent, chan<- struct{}) {
	events := make(chan inputEvent)
	requests := make(chan struct{}, 1)
	reader := bufio.NewReader(input)
	go func() {
		defer close(events)
		for {
			select {
			case <-requests:
			case <-ctx.Done():
				return
			}
			line, err := readLine(reader, maximum)
			select {
			case events <- inputEvent{line: line, err: err}:
			case <-ctx.Done():
				return
			}
			if errors.Is(err, io.EOF) || err != nil && !errors.Is(err, ErrInputTooLong) && !errors.Is(err, ErrInvalidInput) {
				return
			}
		}
	}()
	return events, requests
}

func readLine(reader *bufio.Reader, maximum int) (string, error) {
	var line strings.Builder
	tooLong := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if !tooLong {
			contentBytes := len(fragment)
			if len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
				contentBytes--
				if contentBytes > 0 && fragment[contentBytes-1] == '\r' {
					contentBytes--
				}
			}
			if line.Len()+contentBytes > maximum {
				tooLong = true
			} else {
				line.Write(fragment)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if tooLong {
			return "", ErrInputTooLong
		}
		if errors.Is(err, io.EOF) && line.Len() == 0 {
			return "", io.EOF
		}
		value := strings.TrimSuffix(line.String(), "\n")
		value = strings.TrimSuffix(value, "\r")
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return "", ErrInvalidInput
		}
		return value, nil
	}
}
