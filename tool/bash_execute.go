package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/eul-ai/eul/agent"
)

func (b *Bash) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[bashArguments](arguments)
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

	if b.noSandbox {
		args.Network = true
	}

	if args.Network && !b.noSandbox {
		if b.authorizeNetwork == nil {
			return errorResult(bashToolName, errors.New("network access requires approval, but authorization is unavailable")), nil
		}
		allowed, err := b.authorizeNetwork(ctx, args.Command)
		if err != nil {
			return errorResult(bashToolName, fmt.Errorf("authorize network access: %w", err)), err
		}
		if !allowed {
			return errorResult(bashToolName, errors.New("network access denied")), nil
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command, err := newBashProcess(runCtx, b.shell, args.Command, args.Network)
	if err != nil {
		return errorResult(bashToolName, err), nil
	}
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
	command.WaitDelay = b.waitDelay

	capture := newTailCapture(defaultMaxBytes)
	command.Stdout = capture
	command.Stderr = capture
	started := time.Now()
	var streamer *bashOutputStreamer
	if updates != nil {
		streamer = newBashOutputStreamer(capture, updates, args.Command, started, timeout, cancel)
		command.Stdout = streamer
		command.Stderr = streamer
	}
	if err := command.Start(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			result := errorResult(bashToolName, fmt.Errorf("canceled before shell started; exit status: unavailable: %w", contextErr))
			setFinalBashPresentation(updates, args.Command, "", "", time.Since(started), timeout)
			return result, contextErr
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result := errorResult(bashToolName, fmt.Errorf("timed out after %s before shell started; exit status: unavailable", timeout))
			setFinalBashPresentation(updates, args.Command, "", "", time.Since(started), timeout)
			return result, nil
		}
		operation := "shell"
		if !args.Network {
			operation = "network-isolated shell"
		}
		result := errorResult(bashToolName, fmt.Errorf("failed to start %s: %w; exit status: unavailable", operation, err))
		setFinalBashPresentation(updates, args.Command, "", "", time.Since(started), timeout)
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
	setFinalBashPresentation(updates, args.Command, output, strings.Trim(status, "[]"), time.Since(started), timeout)
	if updateErr != nil {
		return result, updateErr
	}
	if parentErr != nil {
		return result, parentErr
	}
	return result, nil
}
