//go:build linux

package tool

import (
	"context"
	"os"
	"syscall"
	"testing"
)

func TestBashProcessConfiguresNetworkNamespace(t *testing.T) {
	sandboxed, err := newBashProcess(context.Background(), "bash", "true", false)
	if err != nil {
		t.Fatal(err)
	}
	attributes := sandboxed.SysProcAttr
	wantFlags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET)
	if attributes == nil || attributes.Cloneflags != wantFlags || !attributes.Setpgid {
		t.Fatalf("sandboxed process attributes = %+v", attributes)
	}
	if len(attributes.UidMappings) != 1 || attributes.UidMappings[0] != (syscall.SysProcIDMap{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}) {
		t.Fatalf("UID mappings = %+v", attributes.UidMappings)
	}
	if len(attributes.GidMappings) != 1 || attributes.GidMappings[0] != (syscall.SysProcIDMap{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}) || attributes.GidMappingsEnableSetgroups {
		t.Fatalf("GID mappings = %+v, setgroups = %t", attributes.GidMappings, attributes.GidMappingsEnableSetgroups)
	}

	allowed, err := newBashProcess(context.Background(), "bash", "true", true)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.SysProcAttr == nil || allowed.SysProcAttr.Cloneflags != 0 || !allowed.SysProcAttr.Setpgid {
		t.Fatalf("network-enabled process attributes = %+v", allowed.SysProcAttr)
	}
}
