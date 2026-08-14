package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
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
