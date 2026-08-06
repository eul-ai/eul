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
)

var bashToolDefinition = agent.ToolDefinition{
	Name:        bashToolName,
	Description: "Run unsandboxed, noninteractive Bash commands with the user's permissions, a timeout, and bounded output.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"command": {Type: "string", Description: "Command passed exactly to bash -c."},
		"timeout": nullable("integer", "Optional timeout in seconds; null uses the configured default."),
	}, "command", "timeout"),
}

// BashOptions configures the subprocess environment. A nil Env inherits the
// current process environment; a non-nil Env replaces it.
type BashOptions struct {
	Env []string
}

// Bash executes noninteractive shell commands in a fixed working directory.
type Bash struct {
	workspace      workspace
	shell          string
	env            []string
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	waitDelay      time.Duration
}

type bashArguments struct {
	Command string `json:"command"`
	Timeout *int   `json:"timeout"`
}

// NewBash constructs a Bash tool rooted at cwd.
func NewBash(cwd string, options BashOptions) *Bash {
	return &Bash{
		workspace:      newWorkspace(cwd),
		shell:          bashToolName,
		env:            options.Env,
		defaultTimeout: defaultBashTimeout,
		maxTimeout:     maximumBashTimeout,
		waitDelay:      defaultWaitDelay,
	}
}

func (*Bash) Definition() agent.ToolDefinition {
	return bashToolDefinition
}

func (b *Bash) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
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
	if b.env != nil {
		command.Env = b.env
	} else {
		command.Env = os.Environ()
	}
	command.WaitDelay = b.waitDelay

	capture := newTailCapture(defaultMaxBytes)
	command.Stdout = capture
	command.Stderr = capture
	if err := command.Start(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			result := errorResult(bashToolName, fmt.Errorf("canceled before shell started; exit status: unavailable: %w", contextErr))
			return result, contextErr
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return errorResult(bashToolName, fmt.Errorf("timed out after %s before shell started; exit status: unavailable", timeout)), nil
		}
		return errorResult(bashToolName, fmt.Errorf("failed to start shell: %w; exit status: unavailable", err)), nil
	}
	waitErr := command.Wait()
	output, captureTruncated := capture.String()
	exitStatus := 0
	if command.ProcessState != nil {
		exitStatus = command.ProcessState.ExitCode()
	} else {
		exitStatus = -1
	}

	parentErr := ctx.Err()
	localTimeout := parentErr == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded)
	status := fmt.Sprintf("[exit status: %d]", exitStatus)
	isError := waitErr != nil
	if localTimeout {
		status = fmt.Sprintf("[exit status: %d; timed out after %s]", exitStatus, timeout)
		isError = true
	} else if parentErr != nil {
		status = fmt.Sprintf("[exit status: %d; canceled]", exitStatus)
		isError = true
	} else if errors.Is(waitErr, exec.ErrWaitDelay) {
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
	if parentErr != nil {
		return result, parentErr
	}
	return result, nil
}

// tailCapture bounds process output while it is produced. stdout and stderr
// may write concurrently, so all state is protected by a mutex.
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
