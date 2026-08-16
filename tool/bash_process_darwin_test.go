//go:build darwin

package tool

import (
	"context"
	"slices"
	"testing"
)

func TestBashProcessConfiguresNetworkSandbox(t *testing.T) {
	sandboxed, err := newBashProcess(context.Background(), "bash", "true", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/sandbox-exec", "-p", bashNetworkSandboxProfile, "bash", "-c", "true"}
	if sandboxed.Path != "/usr/bin/sandbox-exec" || !slices.Equal(sandboxed.Args, want) {
		t.Fatalf("sandboxed command path=%q args=%q", sandboxed.Path, sandboxed.Args)
	}

	allowed, err := newBashProcess(context.Background(), "bash", "true", true)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Path == "/usr/bin/sandbox-exec" || !slices.Equal(allowed.Args, []string{"bash", "-c", "true"}) {
		t.Fatalf("network-enabled command path=%q args=%q", allowed.Path, allowed.Args)
	}
}
