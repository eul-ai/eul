package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

func TestAgentSessionWiresModelAndTools(t *testing.T) {
	cwd := t.TempDir()
	projectInstructions := "Run focused tests before finishing."
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(projectInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	var gotRequest agent.Request
	factoryCalls := 0
	runtime := testRuntime(cwd, &stdout, &stderr)
	driver := testBackendDriver(t, runtime)
	driver.runtime.newProvider = func() (agent.Provider, error) {
		factoryCalls++
		return providerFunction(func(_ context.Context, request agent.Request, sink agent.TextSink) (agent.Response, error) {
			gotRequest = request
			if err := sink("answer"); err != nil {
				return agent.Response{}, err
			}
			return agent.Response{Text: "answer"}, nil
		}), nil
	}

	config, err := resolveTestConfig(Options{
		Model:         stringPointer("gpt-5.6-sol"),
		ThinkingLevel: agent.ThinkingXHigh,
		FastMode:      true,
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, driver.runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "test prompt", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}

	if driver.runtime.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", driver.runtime.closeCalls)
	}
	if factoryCalls != 1 || gotRequest.Model != "gpt-5.6-sol" || gotRequest.ThinkingLevel != agent.ThinkingXHigh || !gotRequest.FastMode || len(gotRequest.Inputs) != 1 || gotRequest.Inputs[0].PlainText() != "test prompt" {
		t.Fatalf("factory calls=%d request=%+v", factoryCalls, gotRequest)
	}
	if result.Text != "answer" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(gotRequest.Instructions, projectInstructions) || !strings.Contains(gotRequest.Instructions, filepath.ToSlash(filepath.Join(cwd, "AGENTS.md"))) || strings.Count(gotRequest.Instructions, filepath.ToSlash(cwd)) < 2 {
		t.Fatalf("instructions omit project context:\n%s", gotRequest.Instructions)
	}
	names := make([]string, len(gotRequest.Tools))
	for i, definition := range gotRequest.Tools {
		names[i] = definition.Name
	}
	wantNames := []string{"bash", "insert_after", "insert_before", "read", "replace", "subagent", "subagent_cancel", "subagent_wait", "update_goal", "write"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
}

func TestAgentSessionLaunchesAndWaitsForConcurrentSubagents(t *testing.T) {
	cwd := t.TempDir()
	projectInstructions := "Follow project instructions."
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(projectInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr)
	var mu sync.Mutex
	factoryCalls := 0
	childRequests := make([]agent.Request, 0, 2)
	childrenStarted := make(chan struct{})
	releaseChildren := make(chan struct{})
	mainCalls := 0
	driver := testBackendDriver(t, runtime)
	driver.runtime.newProvider = func() (agent.Provider, error) {
		mu.Lock()
		factoryCalls++
		call := factoryCalls
		mu.Unlock()

		if call == 1 {
			return providerFunction(func(_ context.Context, request agent.Request, sink agent.TextSink) (agent.Response, error) {
				mainCalls++
				switch mainCalls {
				case 1:
					if request.ThinkingLevel != agent.ThinkingHigh {
						t.Fatalf("main thinking level = %q", request.ThinkingLevel)
					}
					definitions := make(map[string]agent.ToolDefinition, len(request.Tools))
					for _, definition := range request.Tools {
						definitions[definition.Name] = definition
					}
					waitDefinition := definitions["subagent_wait"]
					timeoutTypes, _ := waitDefinition.Parameters.Properties["timeout_ms"].Type.([]string)
					if definitions["subagent"].Name != "subagent" || waitDefinition.Name != "subagent_wait" || !slices.Equal(timeoutTypes, []string{"integer", "null"}) || definitions["subagent_cancel"].Name != "subagent_cancel" {
						t.Fatalf("subagent definitions = %+v", definitions)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "launch",
						Name:      "subagent",
						Arguments: []byte(`{"tasks":[{"description":"review alpha","prompt":"review alpha","model_profile":"fast","thinking_level":"minimal"},{"description":"review beta","prompt":"review beta","model_profile":"balanced","thinking_level":"medium"}]}`),
					}}}, nil
				case 2:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "subagent" {
						t.Fatalf("launch continuation inputs = %+v", request.Inputs)
					}
					if request.Inputs[0].CallID != "launch" || request.Inputs[0].IsError {
						t.Fatalf("launch result = %+v", request.Inputs[0])
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "read",
						Name:      "read",
						Arguments: []byte(`{"path":"AGENTS.md"}`),
					}}}, nil
				case 3:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "read" || !strings.Contains(request.Inputs[0].PlainText(), projectInstructions) {
						t.Fatalf("independent continuation inputs = %+v", request.Inputs)
					}
					<-childrenStarted
					close(releaseChildren)
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "wait",
						Name:      "subagent_wait",
						Arguments: []byte(`{}`),
					}}}, nil
				case 4:
					if len(request.Inputs) != 2 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].CallID != "wait" || request.Inputs[0].Tool != "subagent_wait" || request.Inputs[0].IsError || request.Inputs[1].Kind != agent.InputInbox || !strings.Contains(request.Inputs[1].PlainText(), "finding for review") {
						t.Fatalf("wait continuation inputs = %+v", request.Inputs)
					}
					if err := sink("combined answer"); err != nil {
						return agent.Response{}, err
					}
					return agent.Response{Text: "combined answer"}, nil
				case 5:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputInbox || !strings.Contains(request.Inputs[0].PlainText(), "finding for review") {
						t.Fatalf("late completion inputs = %+v", request.Inputs)
					}
					return agent.Response{Text: "combined answer"}, nil
				default:
					t.Fatalf("unexpected main provider call %d", mainCalls)
					return agent.Response{}, nil
				}
			}), nil
		}

		return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
			mu.Lock()
			childRequests = append(childRequests, request)
			if len(childRequests) == cap(childRequests) {
				close(childrenStarted)
			}
			mu.Unlock()
			if len(request.Inputs) != 1 {
				t.Fatalf("child inputs = %+v", request.Inputs)
			}
			<-releaseChildren
			return agent.Response{Text: "finding for " + request.Inputs[0].PlainText()}, nil
		}), nil
	}

	config, err := resolveTestConfig(Options{
		Model:         stringPointer("model"),
		FastModel:     stringPointer("fast-model"),
		BalancedModel: stringPointer("balanced-model"),
		ThinkingLevel: agent.ThinkingHigh,
		FastMode:      true,
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, driver.runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "review in parallel", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}
	if driver.runtime.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", driver.runtime.closeCalls)
	}
	if result.Text != "combined answer" || mainCalls < 4 || mainCalls > 5 {
		t.Fatalf("result = %+v, main calls = %d", result, mainCalls)
	}

	mu.Lock()
	defer mu.Unlock()
	if factoryCalls != 3 || len(childRequests) != 2 {
		t.Fatalf("factory calls = %d, child requests = %d", factoryCalls, len(childRequests))
	}
	var tasks []string
	for _, request := range childRequests {
		task := request.Inputs[0].PlainText()
		switch task {
		case "review alpha":
			if request.Model != "fast-model" || request.ThinkingLevel != agent.ThinkingMinimal {
				t.Fatalf("alpha child request = %+v", request)
			}
		case "review beta":
			if request.Model != "balanced-model" || request.ThinkingLevel != agent.ThinkingMedium {
				t.Fatalf("beta child request = %+v", request)
			}
		default:
			t.Fatalf("unexpected child request = %+v", request)
		}
		if !request.FastMode || !strings.Contains(request.Instructions, projectInstructions) || strings.Count(request.Instructions, filepath.ToSlash(cwd)) < 2 {
			t.Fatalf("child request = %+v", request)
		}
		names := make([]string, len(request.Tools))
		for index, definition := range request.Tools {
			names[index] = definition.Name
		}
		wantNames := []string{"read"}
		if !slices.Equal(names, wantNames) {
			t.Fatalf("child tools = %v, want %v", names, wantNames)
		}
		tasks = append(tasks, task)
	}
	slices.Sort(tasks)
	if !slices.Equal(tasks, []string{"review alpha", "review beta"}) {
		t.Fatalf("child tasks = %v", tasks)
	}
}

type steeringWaitProvider func(context.Context, agent.Request) (agent.Response, error)

func (provider steeringWaitProvider) Generate(ctx context.Context, request agent.Request, _ agent.StreamObserver) (agent.Response, error) {
	return provider(ctx, request)
}

func TestSubagentWaitIsInterruptedBySteeringWithoutCancelingChild(t *testing.T) {
	release := make(chan struct{})
	childDone := make(chan struct{})
	manager := subagent.NewManager(subagent.Config{Runner: subagent.RunFunc(func(ctx context.Context, _ subagent.RunRequest, _ func(subagent.Progress)) (agent.RunResult, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-release:
			return agent.RunResult{Text: "done"}, nil
		}
	})})
	defer manager.Close()

	registry, err := tool.NewRegistry([]tool.Tool{tool.NewSubagent(manager), tool.NewSubagentWait(manager)})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := steeringWaitProvider(func(_ context.Context, request agent.Request) (agent.Response, error) {
		calls++
		switch calls {
		case 1:
			return agent.Response{ToolCalls: []agent.ToolCall{{
				ID:        "launch",
				Name:      "subagent",
				Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`),
			}}}, nil
		case 2:
			return agent.Response{ToolCalls: []agent.ToolCall{{
				ID:        "wait",
				Name:      "subagent_wait",
				Arguments: json.RawMessage(`{"timeout_ms":1000}`),
			}}}, nil
		case 3:
			if len(request.Inputs) != 2 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].CallID != "wait" || request.Inputs[0].Tool != "subagent_wait" || request.Inputs[1].Kind != agent.InputUser || request.Inputs[1].PlainText() != "redirect" {
				return agent.Response{}, fmt.Errorf("steering request = %+v", request)
			}
			return agent.Response{Text: "redirected"}, nil
		default:
			return agent.Response{}, fmt.Errorf("unexpected provider call %d", calls)
		}
	})
	bridge := newSubagentBridge(manager, "subagent_wait")
	engine := agent.New(provider, registry, agent.Options{Model: "model", Inbox: bridge, AdditionalInstructions: bridge.additionalInstructions})

	waitStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event agent.Event) error {
			if event.Kind == agent.EventToolStart && event.Call.Name == "subagent_wait" {
				close(waitStarted)
			}
			return nil
		})
		done <- err
	}()
	<-waitStarted
	if !engine.Steer(agent.NewTextInput("redirect").Content) {
		t.Fatal("active engine rejected steering")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-childDone:
		t.Fatal("steering interruption canceled child")
	default:
	}

	close(release)
	<-childDone
}
