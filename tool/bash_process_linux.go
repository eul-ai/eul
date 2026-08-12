//go:build linux

package tool

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func newBashProcess(ctx context.Context, shell, source string, network bool) (*exec.Cmd, error) {
	command := exec.CommandContext(ctx, shell, "-c", source)
	attributes := &syscall.SysProcAttr{Setpgid: true}
	if network {
		command.SysProcAttr = attributes
		return command, nil
	}

	uid := os.Getuid()
	gid := os.Getgid()
	attributes.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	attributes.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
	attributes.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
	attributes.GidMappingsEnableSetgroups = false
	command.SysProcAttr = attributes
	return command, nil
}
