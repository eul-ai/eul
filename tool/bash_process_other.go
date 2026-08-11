//go:build !linux && !darwin

package tool

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func newBashProcess(ctx context.Context, shell, source string, network bool) (*exec.Cmd, error) {
	if !network {
		return nil, errors.New("network isolation is only supported on Linux and macOS")
	}

	command := exec.CommandContext(ctx, shell, "-c", source)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command, nil
}
