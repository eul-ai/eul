package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/eul-ai/eul/agent"
)

func (b bashTool) run(ctx context.Context, args bashArguments, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if strings.TrimSpace(args.Command) == "" {
		return agent.ToolResult{}, errors.New("command is required and must be nonempty")
	}

	timeout := defaultBashTimeout
	if args.Timeout != nil {
		seconds, err := args.Timeout.Int64()
		if err != nil {
			return agent.ToolResult{}, errors.New("timeout must be an integer")
		}
		if seconds <= 0 {
			return agent.ToolResult{}, errors.New("timeout must be positive")
		}
		if time.Duration(seconds) > maximumBashTimeout/time.Second {
			return agent.ToolResult{}, fmt.Errorf("timeout must not exceed %s (requested: %ds)", maximumBashTimeout, seconds)
		}
		timeout = time.Duration(seconds) * time.Second
	}

	switch {
	case !b.sandboxed():
		args.Network = true
	case args.Network:
		allowed, err := b.authorizeNetwork(ctx, args.Command)
		if err != nil {
			return agent.ToolResult{}, fmt.Errorf("authorize network access: %w", err)
		}
		if !allowed {
			return agent.ToolResult{}, errors.New("network access denied")
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := newBashProcess(runCtx, bashShell, args.Command, args.Network)
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Dir = b.workspace.cwd
	command.Stdin = nil
	command.Env = os.Environ()
	command.WaitDelay = bashWaitDelay

	capture := newTailCapture(defaultMaxBytes)
	started := time.Now()
	streamer := newBashOutputStreamer(capture, updates, args.Command, started, timeout, cancel)
	command.Stdout = streamer
	command.Stderr = streamer
	if err := command.Start(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			result := errorResult(bashToolName, fmt.Errorf("canceled before shell started; exit status: unavailable: %w", contextErr))
			setFinalBashPresentation(updates, args.Command, "", "", time.Since(started), timeout)
			return result, contextErr
		}
		setFinalBashPresentation(updates, args.Command, "", "", time.Since(started), timeout)
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return agent.ToolResult{}, fmt.Errorf("timed out after %s before shell started; exit status: unavailable", timeout)
		}
		operation := "shell"
		if !args.Network {
			operation = "network-isolated shell"
		}
		return agent.ToolResult{}, fmt.Errorf("failed to start %s: %w; exit status: unavailable", operation, err)
	}
	streamer.start()

	waitErr := command.Wait()
	streamer.stop()
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
		status = fmt.Sprintf("[exit status: %d; output pipes remained open past %s]", exitStatus, bashWaitDelay)
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
	setFinalBashPresentation(updates, args.Command, output, strings.Trim(status, "[]"), time.Since(started), timeout)
	return result, parentErr
}
