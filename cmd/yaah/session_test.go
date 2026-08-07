package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"yaah/agent"
	"yaah/tool"
)

type closeRecordingTool struct {
	closeErr error
	closed   int
}

func (*closeRecordingTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "close-recording"}
}

func (*closeRecordingTool) Execute(context.Context, json.RawMessage, agent.ToolUpdateSink) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

func (current *closeRecordingTool) Close() error {
	current.closed++
	return current.closeErr
}

func TestFinishRegistryClosesToolsAndPreservesRunError(t *testing.T) {
	runErr := context.Canceled
	closeErr := errors.New("close failed")
	closer := &closeRecordingTool{closeErr: closeErr}
	registry := tool.NewRegistry(closer)

	err := finishRegistry(runErr, registry, "close subagent tools")
	if closer.closed != 1 {
		t.Fatalf("close calls = %d, want 1", closer.closed)
	}
	if !errors.Is(err, runErr) || !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "close subagent tools") {
		t.Fatalf("joined error = %v", err)
	}
}

func TestFinishRunReportsCleanupFailureJoinedWithInterruption(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	var output bytes.Buffer

	code := finishRun(errors.Join(context.Canceled, cleanupErr), &output)
	if code != exitFailure || !strings.Contains(output.String(), cleanupErr.Error()) {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestAgentSessionFinishClosesOwnedRegistry(t *testing.T) {
	closer := &closeRecordingTool{}
	session := &agentSession{tools: tool.NewRegistry(closer)}
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}
	if closer.closed != 1 {
		t.Fatalf("close calls = %d, want 1", closer.closed)
	}
}
