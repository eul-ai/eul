package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

func TestAgentSessionRoutesNetworkApprovalToTerminal(t *testing.T) {
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	session, err := newAgentSession(resolvedConfig{models: modelSet{main: "model", fast: "model", balanced: "model"}, cwd: t.TempDir()}, environment{}, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)

	type outcome struct {
		result agent.ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := session.tools.Execute(context.Background(), agent.ToolCall{
			ID:        "bash-network",
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"printf approved","timeout":null,"network":true}`),
		}, nil)
		done <- outcome{result: result, err: err}
	}()

	select {
	case request := <-session.terminalOptions.Events.PermissionRequests:
		if request.Subject != "bash" || request.Detail != "printf approved" {
			t.Fatalf("request = %+v", request)
		}
		request.Response <- terminal.PermissionAllowOnce
	case <-time.After(time.Second):
		t.Fatal("terminal did not receive network permission request")
	}
	select {
	case completed := <-done:
		if completed.err != nil || completed.result.IsError || !strings.Contains(completed.result.Output, "approved") {
			t.Fatalf("result = %+v, error = %v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("approved bash command did not finish")
	}
}

func TestAgentSessionRoutesSubagentNetworkApprovalToTerminal(t *testing.T) {
	var providerCount atomic.Int32
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		if providerCount.Add(1) == 1 {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		}
		return networkBashChildProvider("child approved", nil), nil
	}}
	session, err := newAgentSession(resolvedConfig{models: modelSet{main: "model", fast: "model", balanced: "model"}, cwd: t.TempDir()}, environment{}, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)

	result, err := session.tools.Execute(context.Background(), agent.ToolCall{
		ID:        "launch",
		Name:      "launch_subagents",
		Arguments: json.RawMessage(`{"tasks":[{"description":"inspect network","prompt":"inspect network"}]}`),
	}, nil)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}

	select {
	case request := <-session.terminalOptions.Events.PermissionRequests:
		if request.Subject != "bash" || request.Detail != "printf child" {
			t.Fatalf("request = %+v", request)
		}
		status := session.subagents.Snapshot()
		if len(status.Active) != 1 || len(status.PendingCompletions) != 0 {
			t.Fatalf("subagent did not wait for permission: %+v", status)
		}
		select {
		case request.Response <- terminal.PermissionAllowOnce:
		case <-time.After(time.Second):
			t.Fatal("subagent stopped waiting for permission")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal did not receive subagent network permission request")
	}

	waitForSubagentCompletions(t, session.subagents, 1)
	completion := session.subagents.Snapshot().PendingCompletions[0]
	if completion.Status != subagent.StateComplete || completion.Result != "child approved" {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestAgentSessionSharesNetworkSessionApprovalWithSubagents(t *testing.T) {
	childCompleted := make(chan struct{})
	var providerCount atomic.Int32
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		if providerCount.Add(1) == 1 {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		}
		return networkBashChildProvider("child approved", childCompleted), nil
	}}
	session, err := newAgentSession(resolvedConfig{models: modelSet{main: "model", fast: "model", balanced: "model"}, cwd: t.TempDir()}, environment{}, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)

	type outcome struct {
		result agent.ToolResult
		err    error
	}
	parentDone := make(chan outcome, 1)
	go func() {
		result, err := session.tools.Execute(context.Background(), agent.ToolCall{
			ID:        "parent-bash",
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"printf parent","timeout":null,"network":true}`),
		}, nil)
		parentDone <- outcome{result: result, err: err}
	}()

	select {
	case request := <-session.terminalOptions.Events.PermissionRequests:
		request.Response <- terminal.PermissionAllowSession
	case <-time.After(time.Second):
		t.Fatal("terminal did not receive parent network permission request")
	}
	select {
	case completed := <-parentDone:
		if completed.err != nil || completed.result.IsError {
			t.Fatalf("parent result = %+v, error = %v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent Bash did not finish")
	}

	result, err := session.tools.Execute(context.Background(), agent.ToolCall{
		ID:        "launch",
		Name:      "launch_subagents",
		Arguments: json.RawMessage(`{"tasks":[{"description":"inspect network","prompt":"inspect network"}]}`),
	}, nil)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}

	select {
	case request := <-session.terminalOptions.Events.PermissionRequests:
		request.Response <- terminal.PermissionDenyOnce
		t.Fatal("subagent requested permission after session approval")
	case <-childCompleted:
	case <-time.After(time.Second):
		t.Fatal("subagent Bash did not inherit session approval")
	}
	waitForSubagentCompletions(t, session.subagents, 1)
}

func networkBashChildProvider(result string, completed chan<- struct{}) agent.Provider {
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

func TestNetworkPermissionBrokerSkipsPermissions(t *testing.T) {
	authorize, requests := newNetworkPermissionBroker(true)
	allowed, err := authorize(context.Background(), "git push")
	if !allowed || err != nil || requests != nil {
		t.Fatalf("allowed = %t, requests = %v, error = %v", allowed, requests, err)
	}
}

func TestNetworkPermissionBrokerReturnsDecision(t *testing.T) {
	authorize, requests := newNetworkPermissionBroker(false)
	type outcome struct {
		allowed bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		allowed, err := authorize(context.Background(), "git push")
		done <- outcome{allowed: allowed, err: err}
	}()

	request := <-requests
	if request.Subject != "bash" || request.Detail != "git push" {
		t.Fatalf("request = %+v", request)
	}
	request.Response <- terminal.PermissionAllowOnce
	result := <-done
	if !result.allowed || result.err != nil {
		t.Fatalf("outcome = %+v", result)
	}
}

func TestNetworkPermissionBrokerAllowsRemainingSession(t *testing.T) {
	authorize, requests := newNetworkPermissionBroker(false)
	type outcome struct {
		allowed bool
		err     error
	}
	authorizeAsync := func(command string) <-chan outcome {
		done := make(chan outcome, 1)
		go func() {
			allowed, err := authorize(context.Background(), command)
			done <- outcome{allowed: allowed, err: err}
		}()
		return done
	}

	first := authorizeAsync("git push")
	request := <-requests
	request.Response <- terminal.PermissionAllowSession
	if result := <-first; !result.allowed || result.err != nil {
		t.Fatalf("first outcome = %+v", result)
	}

	second := authorizeAsync("ssh host")
	select {
	case request := <-requests:
		request.Response <- terminal.PermissionDenyOnce
		t.Fatal("second authorization requested permission")
	case result := <-second:
		if !result.allowed || result.err != nil {
			t.Fatalf("second outcome = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("second authorization did not finish")
	}
}

func TestNetworkPermissionBrokerHonorsCancellation(t *testing.T) {
	authorize, requests := newNetworkPermissionBroker(false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := authorize(ctx, "ssh host")
		done <- err
	}()

	<-requests
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("authorization did not stop after cancellation")
	}
}
