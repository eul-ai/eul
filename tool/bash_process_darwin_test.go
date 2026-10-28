//go:build darwin

package tool

import (
	"context"
	"slices"
	"testing"
)

func TestBashProcessConfiguresNetworkSandbox(t *testing.T) {
	sandboxed := newBashProcess(context.Background(), "bash", "true", false)
	want := []string{"/usr/bin/sandbox-exec", "-p", bashNetworkSandboxProfile, "bash", "-c", "true"}
	if sandboxed.Path != "/usr/bin/sandbox-exec" || !slices.Equal(sandboxed.Args, want) {
		t.Fatalf("sandboxed command path=%q args=%q", sandboxed.Path, sandboxed.Args)
	}

	allowed := newBashProcess(context.Background(), "bash", "true", true)
	if allowed.Path == "/usr/bin/sandbox-exec" || !slices.Equal(allowed.Args, []string{"bash", "-c", "true"}) {
		t.Fatalf("network-enabled command path=%q args=%q", allowed.Path, allowed.Args)
	}
}
