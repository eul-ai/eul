//go:build linux

package tool

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func newBashProcess(ctx context.Context, source string, network bool) *exec.Cmd {
	command := exec.CommandContext(ctx, bashShell, "-c", source)
	attributes := &syscall.SysProcAttr{Setpgid: true}
	if network {
		command.SysProcAttr = attributes
		return command
	}

	uid := os.Getuid()
	gid := os.Getgid()
	attributes.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	attributes.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
	attributes.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
	attributes.GidMappingsEnableSetgroups = false
	command.SysProcAttr = attributes
	return command
}
