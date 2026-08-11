//go:build darwin

package tool

import (
	"context"
	"os/exec"
	"syscall"
)

const bashNetworkSandboxProfile = `(version 1)
(allow default)
(deny network*)`

func newBashProcess(ctx context.Context, shell, source string, network bool) (*exec.Cmd, error) {
	var command *exec.Cmd
	if network {
		command = exec.CommandContext(ctx, shell, "-c", source)
	} else {
		command = exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", bashNetworkSandboxProfile, shell, "-c", source)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command, nil
}
