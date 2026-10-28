package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
)

type toolOutcome struct {
	result agent.Input
	err    error
}

func callToolAsync(caller *toolCaller, opened *agentSession, name, arguments string) <-chan toolOutcome {
	done := make(chan toolOutcome, 1)
	go func() {
		result, err := caller.call(opened, name, arguments)
		done <- toolOutcome{result: result, err: err}
	}()
	return done
}

func awaitPermission(t *testing.T, conversation *session.Session) *session.PermissionRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if requests := conversation.Permissions(); len(requests) > 0 {
			return requests[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session did not receive a network permission request")
	return nil
}

func TestAgentSessionAsksForNetworkApproval(t *testing.T) {
	caller := &toolCaller{}
	cwd := t.TempDir()
	opened := startTestSession(t, testLauncher(t, cwd, caller.runtime()), session.Settings{Models: backend.Models{Main: "model"}, WorkingDirectory: cwd})
	defer opened.finish(nil)

	done := callToolAsync(caller, opened, "bash", `{"command":"printf approved","timeout":null,"network":true}`)
	if request := awaitPermission(t, opened.conversation); request.Subject != "bash" || request.Detail != "printf approved" {
		t.Fatalf("request = %+v", request)
	}
	opened.conversation.ResolvePermission(session.PermissionAllowOnce)
	select {
	case completed := <-done:
		if completed.err != nil || completed.result.IsError || !strings.Contains(completed.result.PlainText(), "approved") {
			t.Fatalf("result = %+v, error = %v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("approved bash command did not finish")
	}
}

func TestAgentSessionAsksForSubagentNetworkApproval(t *testing.T) {
	caller := &toolCaller{child: networkBashChildProvider("child approved", nil)}
	cwd := t.TempDir()
	opened := startTestSession(t, testLauncher(t, cwd, caller.runtime()), session.Settings{Models: backend.Models{Main: "model"}, WorkingDirectory: cwd})
	defer opened.finish(nil)

	result, err := caller.call(opened, "launch_subagents", `{"tasks":[{"description":"inspect network","prompt":"inspect network"}]}`)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}

	if request := awaitPermission(t, opened.conversation); request.Subject != "bash" || request.Detail != "printf child" {
		t.Fatalf("request = %+v", request)
	}
	if status := opened.subagents.Snapshot(); len(status.Active) != 1 || len(status.PendingCompletions) != 0 {
		t.Fatalf("subagent did not wait for permission: %+v", status)
	}
	opened.conversation.ResolvePermission(session.PermissionAllowOnce)

	waitForSubagentCompletions(t, opened.subagents, 1)
	completion := opened.subagents.Snapshot().PendingCompletions[0]
	if completion.Status != subagent.StateComplete || completion.Result != "child approved" {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestAgentSessionSharesNetworkSessionApprovalWithSubagents(t *testing.T) {
	childCompleted := make(chan struct{})
	caller := &toolCaller{child: networkBashChildProvider("child approved", childCompleted)}
	cwd := t.TempDir()
	opened := startTestSession(t, testLauncher(t, cwd, caller.runtime()), session.Settings{Models: backend.Models{Main: "model"}, WorkingDirectory: cwd})
	defer opened.finish(nil)

	parentDone := callToolAsync(caller, opened, "bash", `{"command":"printf parent","timeout":null,"network":true}`)
	awaitPermission(t, opened.conversation)
	opened.conversation.ResolvePermission(session.PermissionAllowSession)
	select {
	case completed := <-parentDone:
		if completed.err != nil || completed.result.IsError {
			t.Fatalf("parent result = %+v, error = %v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent Bash did not finish")
	}

	result, err := caller.call(opened, "launch_subagents", `{"tasks":[{"description":"inspect network","prompt":"inspect network"}]}`)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}

	select {
	case <-childCompleted:
	case <-time.After(2 * time.Second):
		t.Fatal("subagent Bash did not inherit session approval")
	}
	if requests := opened.conversation.Permissions(); len(requests) != 0 {
		t.Fatalf("subagent requested permission after session approval: %+v", requests)
	}
	waitForSubagentCompletions(t, opened.subagents, 1)
}

func TestUnsandboxedSessionRunsNetworkCommandsWithoutAsking(t *testing.T) {
	caller := &toolCaller{}
	cwd := t.TempDir()
	launcher := testLauncher(t, cwd, caller.runtime())
	launcher.noSandbox = true
	opened := startTestSession(t, launcher, session.Settings{Models: backend.Models{Main: "model"}, WorkingDirectory: cwd})
	defer opened.finish(nil)

	result, err := caller.call(opened, "bash", `{"command":"printf open","timeout":null,"network":true}`)
	if err != nil || result.IsError || !strings.Contains(result.PlainText(), "open") || len(opened.conversation.Permissions()) != 0 {
		t.Fatalf("result = %+v, error = %v, permissions = %d", result, err, len(opened.conversation.Permissions()))
	}
}

func networkBashChildProvider(result string, completed chan<- struct{}) providerFunction {
	calls := 0
	return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
		calls++
		switch calls {
		case 1:
			return agent.Response{ToolCalls: []agent.ToolCall{{
				ID:        "child-bash",
				Name:      "bash",
				Arguments: json.RawMessage(`{"command":"printf child","timeout":null,"network":true}`),
			}}}, nil
		case 2:
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "bash" || request.Inputs[0].IsError {
				return agent.Response{}, fmt.Errorf("child Bash result = %+v", request.Inputs)
			}
			if completed != nil {
				close(completed)
			}
			return agent.Response{Text: result}, nil
		default:
			return agent.Response{}, fmt.Errorf("unexpected child provider call %d", calls)
		}
	})
}
