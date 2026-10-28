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

func newBashProcess(ctx context.Context, source string, network bool) *exec.Cmd {
	var command *exec.Cmd
	if network {
		command = exec.CommandContext(ctx, bashShell, "-c", source)
	} else {
		command = exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", bashNetworkSandboxProfile, bashShell, "-c", source)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command
}
