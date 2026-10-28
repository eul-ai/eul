package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	bashToolName       = "bash"
	bashShell          = "bash"
	defaultBashTimeout = 120 * time.Second
	maximumBashTimeout = time.Hour
	bashWaitDelay      = time.Second
	bashPreviewLines   = 5
	bashUpdateInterval = 100 * time.Millisecond
)

var bashToolDefinition = agent.ToolDefinition{
	Name:        bashToolName,
	Description: "Run a noninteractive Bash command.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"command": {Type: "string", Description: "Command passed to bash -c."},
		"timeout": nullable("integer", fmt.Sprintf("Timeout in seconds; null uses the default (%d seconds).", defaultBashTimeout/time.Second)),
		"network": {Type: "boolean", Description: "Allow network access; true requires approval."},
	}, "command", "timeout", "network"),
}

type NetworkAuthorizer func(context.Context, string) (bool, error)

type bashTool struct {
	workspace        workspace
	authorizeNetwork NetworkAuthorizer
}

type bashArguments struct {
	Command string       `json:"command"`
	Timeout *json.Number `json:"timeout"`
	Network bool         `json:"network"`
}

// NewBash returns an unsandboxed Bash tool when authorizeNetwork is nil.
func NewBash(cwd string, authorizeNetwork NetworkAuthorizer) Tool {
	bash := bashTool{workspace: newWorkspace(cwd), authorizeNetwork: authorizeNetwork}
	return spec[bashArguments]{
		definition: bashToolDefinition,
		present:    presentBash,
		run:        bash.run,
	}
}

func (b bashTool) sandboxed() bool {
	return b.authorizeNetwork != nil
}

func presentBash(args bashArguments) agent.ToolPresentation {
	timeout := defaultBashTimeout
	if args.Timeout != nil {
		if seconds, err := args.Timeout.Int64(); err == nil {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return bashPresentation(args.Command, timeout)
}

func bashPresentation(command string, timeout time.Duration) agent.ToolPresentation {
	return agent.ToolPresentation{Title: bashToolName, Arguments: displayToolArgument(command), Timeout: timeout}
}

func (b bashTool) run(ctx context.Context, args bashArguments, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if strings.TrimSpace(args.Command) == "" {
		return agent.ToolResult{}, errors.New("command is required and must be nonempty")
	}
	timeout, err := bashTimeout(args.Timeout)
	if err != nil {
		return agent.ToolResult{}, err
	}
	network, err := b.allowNetwork(ctx, args)
	if err != nil {
		return agent.ToolResult{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	capture := &tailCapture{}
	command := b.command(runCtx, args.Command, network, capture)
	started := time.Now()
	setFinal := func(output, outcome string) {
		updates.SetFinal(bashOutputPresentation(args.Command, output, outcome, time.Since(started), timeout))
	}

	if err := command.Start(); err != nil {
		setFinal("", "")
		shell := "network-isolated shell"
		if network {
			shell = "shell"
		}
		return agent.ToolResult{}, fmt.Errorf("failed to start %s: %w; exit status: unavailable", shell, err)
	}
	stop := streamBashOutput(capture, started, func(output string, elapsed time.Duration) error {
		err := updates.Update(bashOutputPresentation(args.Command, output, "", elapsed, timeout))
		if err != nil {
			cancel()
		}
		return err
	})
	waitErr := command.Wait()
	stop()

	output, truncated := capture.String()
	if output != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	status, isError := bashStatus(ctx, runCtx, command.ProcessState, waitErr, timeout)
	notice := ""
	if truncated {
		notice = "earlier command output truncated"
	}
	setFinal(output, status)
	return agent.ToolResult{Output: bound(output+"["+status+"]", notice, true), IsError: isError}, ctx.Err()
}

func bashTimeout(value *json.Number) (time.Duration, error) {
	if value == nil {
		return defaultBashTimeout, nil
	}

	seconds, err := value.Int64()
	switch {
	case err != nil:
		return 0, errors.New("timeout must be an integer")
	case seconds <= 0:
		return 0, errors.New("timeout must be positive")
	case time.Duration(seconds) > maximumBashTimeout/time.Second:
		return 0, fmt.Errorf("timeout must not exceed %s (requested: %ds)", maximumBashTimeout, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

func (b bashTool) allowNetwork(ctx context.Context, args bashArguments) (bool, error) {
	switch {
	case !b.sandboxed():
		return true, nil
	case !args.Network:
		return false, nil
	}

	allowed, err := b.authorizeNetwork(ctx, args.Command)
	if err != nil {
		return false, fmt.Errorf("authorize network access: %w", err)
	}
	if !allowed {
		return false, errors.New("network access denied")
	}
	return true, nil
}

func (b bashTool) command(ctx context.Context, source string, network bool, output io.Writer) *exec.Cmd {
	command := newBashProcess(ctx, source, network)
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Dir = b.workspace.cwd
	command.Env = os.Environ()
	command.WaitDelay = bashWaitDelay
	command.Stdout = output
	command.Stderr = output
	return command
}

func bashStatus(ctx, runCtx context.Context, state *os.ProcessState, waitErr error, timeout time.Duration) (string, bool) {
	status := fmt.Sprintf("exit status: %d", state.ExitCode())
	switch {
	case ctx.Err() != nil:
		return status + "; canceled", true
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("%s; timed out after %s", status, timeout), true
	case errors.Is(waitErr, exec.ErrWaitDelay):
		return fmt.Sprintf("%s; output pipes remained open past %s", status, bashWaitDelay), true
	default:
		return status, waitErr != nil
	}
}
