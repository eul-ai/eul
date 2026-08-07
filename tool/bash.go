package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"yaah/agent"
)

const (
	bashToolName       = "bash"
	defaultBashTimeout = 120 * time.Second
	maximumBashTimeout = 10 * time.Minute
	defaultWaitDelay   = time.Second
	bashPreviewLines   = 5
	bashUpdateInterval = 100 * time.Millisecond
)

var bashToolDefinition = agent.ToolDefinition{
	Name:        bashToolName,
	Description: "Run unsandboxed, noninteractive Bash commands with the user's permissions, a timeout, and bounded output.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"command": {Type: "string", Description: "Command passed exactly to bash -c."},
		"timeout": nullable("integer", "Optional timeout in seconds; null uses the configured default."),
	}, "command", "timeout"),
}

type Bash struct {
	workspace      workspace
	shell          string
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	waitDelay      time.Duration
}

type bashArguments struct {
	Command string `json:"command"`
	Timeout *int   `json:"timeout"`
}

func NewBash(cwd string) *Bash {
	return &Bash{
		workspace:      newWorkspace(cwd),
		shell:          bashToolName,
		defaultTimeout: defaultBashTimeout,
		maxTimeout:     maximumBashTimeout,
		waitDelay:      defaultWaitDelay,
	}
}

func (*Bash) Definition() agent.ToolDefinition {
	return bashToolDefinition
}

func (*Bash) Presentation(snapshot agent.ToolCallSnapshot) agent.ToolPresentation {
	return bashPresentation(snapshotString(snapshot, "command"))
}

func bashPresentation(command string) agent.ToolPresentation {
	arguments := ""
	if command != "" {
		arguments = displayToolArgument(command)
	}
	return agent.ToolPresentation{Title: bashToolName, Arguments: arguments}
}

func (b *Bash) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[bashArguments](arguments)
	if err != nil {
		return errorResult(bashToolName, err), nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return errorResult(bashToolName, fmt.Errorf("command is required and must be nonempty")), nil
	}

	timeout := b.defaultTimeout
	if args.Timeout != nil {
		if *args.Timeout <= 0 {
			return errorResult(bashToolName, fmt.Errorf("timeout must be positive")), nil
		}
		if time.Duration(*args.Timeout) > b.maxTimeout/time.Second {
			return errorResult(bashToolName, fmt.Errorf("timeout must not exceed %s", b.maxTimeout)), nil
		}
		timeout = time.Duration(*args.Timeout) * time.Second
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(runCtx, b.shell, "-c", args.Command)
	command.Dir = b.workspace.cwd
	command.Stdin = nil
	command.Env = os.Environ()
	command.WaitDelay = b.waitDelay

	capture := newTailCapture(defaultMaxBytes)
	command.Stdout = capture
	command.Stderr = capture
	started := time.Now()
	var streamer *bashOutputStreamer
	if updates != nil {
		streamer = newBashOutputStreamer(capture, updates, args.Command, started, cancel)
		command.Stdout = streamer
		command.Stderr = streamer
	}
	if err := command.Start(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			result := errorResult(bashToolName, fmt.Errorf("canceled before shell started; exit status: unavailable: %w", contextErr))
			setFinalBashPresentation(updates, args.Command, "", "", time.Since(started))
			return result, contextErr
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result := errorResult(bashToolName, fmt.Errorf("timed out after %s before shell started; exit status: unavailable", timeout))
			setFinalBashPresentation(updates, args.Command, "", "", time.Since(started))
			return result, nil
		}
		result := errorResult(bashToolName, fmt.Errorf("failed to start shell: %w; exit status: unavailable", err))
		setFinalBashPresentation(updates, args.Command, "", "", time.Since(started))
		return result, nil
	}
	if streamer != nil {
		streamer.start()
	}

	waitErr := command.Wait()
	var updateErr error
	if streamer != nil {
		updateErr = streamer.stop()
	}
	output, captureTruncated := capture.String()
	exitStatus := -1
	if command.ProcessState != nil {
		exitStatus = command.ProcessState.ExitCode()
	}

	parentErr := ctx.Err()
	localTimeout := parentErr == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded)
	status := fmt.Sprintf("[exit status: %d]", exitStatus)
	isError := waitErr != nil
	switch {
	case localTimeout:
		status = fmt.Sprintf("[exit status: %d; timed out after %s]", exitStatus, timeout)
		isError = true
	case parentErr != nil:
		status = fmt.Sprintf("[exit status: %d; canceled]", exitStatus)
		isError = true
	case errors.Is(waitErr, exec.ErrWaitDelay):
		status = fmt.Sprintf("[exit status: %d; output pipes remained open past %s]", exitStatus, b.waitDelay)
		isError = true
	}

	if output != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	text := output + status
	notice := ""
	if captureTruncated {
		notice = "earlier command output truncated"
	}
	result := agent.ToolResult{Output: boundTail(text, notice), IsError: isError}
	setFinalBashPresentation(updates, args.Command, output, strings.Trim(status, "[]"), time.Since(started))
	if updateErr != nil {
		return result, updateErr
	}
	if parentErr != nil {
		return result, parentErr
	}
	return result, nil
}

func setFinalBashPresentation(updates agent.ToolUpdateSink, command, output, outcome string, elapsed time.Duration) {
	if updates != nil {
		updates.SetFinal(bashOutputPresentation(command, output, outcome, elapsed))
	}
}

func bashOutputPresentation(command, output, outcome string, elapsed time.Duration) agent.ToolPresentation {
	presentation := bashPresentation(command)
	presentation.Outcome = outcome
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		presentation.Lines = strings.Split(trimmed, "\n")
	}
	presentation.TailLines = bashPreviewLines
	presentation.Elapsed = max(time.Nanosecond, elapsed)
	return presentation
}

type bashOutputStreamer struct {
	capture  *tailCapture
	updates  agent.ToolUpdateSink
	command  string
	started  time.Time
	cancel   context.CancelFunc
	dirty    chan struct{}
	stopNow  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
	errMu    sync.Mutex
	err      error
}

func newBashOutputStreamer(
	capture *tailCapture,
	updates agent.ToolUpdateSink,
	command string,
	started time.Time,
	cancel context.CancelFunc,
) *bashOutputStreamer {
	streamer := &bashOutputStreamer{
		capture: capture,
		updates: updates,
		command: command,
		started: started,
		cancel:  cancel,
		dirty:   make(chan struct{}, 1),
		stopNow: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	return streamer
}

func (s *bashOutputStreamer) start() {
	go s.run()
}

func (s *bashOutputStreamer) Write(data []byte) (int, error) {
	written, err := s.capture.Write(data)
	if written > 0 {
		select {
		case s.dirty <- struct{}{}:
		default:
		}
	}
	return written, err
}

func (s *bashOutputStreamer) run() {
	defer close(s.stopped)
	ticker := time.NewTicker(bashUpdateInterval)
	defer ticker.Stop()

	dirty := false
	lastElapsedSecond := int64(-1)
	for {
		select {
		case <-s.dirty:
			dirty = true
		case now := <-ticker.C:
			elapsed := now.Sub(s.started)
			elapsedSecond := int64(elapsed / time.Second)
			if !dirty && elapsedSecond == lastElapsedSecond {
				continue
			}
			output, _ := s.capture.String()
			if err := s.updates.Update(bashOutputPresentation(s.command, output, "", elapsed)); err != nil {
				s.errMu.Lock()
				s.err = err
				s.errMu.Unlock()
				s.cancel()
				return
			}
			dirty = false
			lastElapsedSecond = elapsedSecond
		case <-s.stopNow:
			return
		}
	}
}

func (s *bashOutputStreamer) stop() error {
	s.stopOnce.Do(func() {
		close(s.stopNow)
	})
	<-s.stopped
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

type tailCapture struct {
	mu        sync.Mutex
	data      []byte
	maxBytes  int
	truncated bool
}

func newTailCapture(maxBytes int) *tailCapture {
	return &tailCapture{maxBytes: maxBytes}
}

func (c *tailCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	originalLength := len(data)
	if c.maxBytes <= 0 {
		c.truncated = c.truncated || originalLength > 0
		return originalLength, nil
	}

	if len(data) > c.maxBytes {
		c.data = append(c.data[:0], data[len(data)-c.maxBytes:]...)
		c.truncated = true
		return originalLength, nil
	}

	if len(c.data)+len(data) > c.maxBytes {
		drop := len(c.data) + len(data) - c.maxBytes
		copy(c.data, c.data[drop:])
		c.data = c.data[:len(c.data)-drop]
		c.truncated = true
	}

	c.data = append(c.data, data...)
	return originalLength, nil
}

func (c *tailCapture) String() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.ToValidUTF8(string(c.data), "�"), c.truncated
}
