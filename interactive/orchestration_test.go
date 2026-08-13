package interactive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestBuildToolset(t *testing.T) {
	for _, test := range []struct {
		name   string
		access toolAccess
		want   []string
	}{
		{name: "full access", access: fullToolAccess, want: []string{"bash", "edit", "read", "write"}},
		{name: "read only", access: readOnlyToolAccess, want: []string{"read"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := buildToolset(t.TempDir(), test.access, true, nil)
			if err != nil {
				t.Fatal(err)
			}

			names := make([]string, len(registry.Definitions()))
			for index, definition := range registry.Definitions() {
				names[index] = definition.Name
			}
			if !slices.Equal(names, test.want) {
				t.Fatalf("tools = %v, want %v", names, test.want)
			}
		})
	}
}

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
	if factoryCalls != 1 || gotRequest.Model != "gpt-5.6-sol" || gotRequest.ThinkingLevel != agent.ThinkingXHigh || !gotRequest.FastMode || len(gotRequest.Inputs) != 1 || gotRequest.Inputs[0].Text != "test prompt" {
		t.Fatalf("factory calls=%d request=%+v", factoryCalls, gotRequest)
	}
	if result.Text != "answer" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(gotRequest.Instructions, projectInstructions) || !strings.Contains(gotRequest.Instructions, filepath.ToSlash(filepath.Join(cwd, "AGENTS.md"))) || !strings.Contains(gotRequest.Instructions, "Current working directory: "+filepath.ToSlash(cwd)) {
		t.Fatalf("instructions omit project context:\n%s", gotRequest.Instructions)
	}
	names := make([]string, len(gotRequest.Tools))
	for i, definition := range gotRequest.Tools {
		names[i] = definition.Name
	}
	wantNames := []string{"bash", "edit", "read", "subagent", "subagent_cancel", "subagent_wait", "update_goal", "write"}
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
	var childRequests []agent.Request
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
					if !strings.Contains(definitions["subagent"].Description, "delivered automatically") || !strings.Contains(waitDefinition.Description, "Wait sparingly") || len(waitDefinition.Parameters.Properties["timeout_ms"].AnyOf) != 2 || definitions["subagent_cancel"].Name == "" {
						t.Fatalf("subagent definitions = %+v", definitions)
					}
					if strings.Contains(request.Instructions, "explicitly asks for subagents") {
						t.Fatalf("main request retains explicit subagent rule: %+v", request)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "launch",
						Name:      "subagent",
						Arguments: []byte(`{"tasks":[{"description":"review alpha","prompt":"review alpha"},{"description":"review beta","prompt":"review beta"}]}`),
					}}}, nil
				case 2:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "subagent" {
						t.Fatalf("launch continuation inputs = %+v", request.Inputs)
					}
					output := request.Inputs[0].Text
					if !strings.Contains(output, "Started subagents (model: balanced, thinking: low)") || !strings.Contains(output, "subagent-1: review alpha") || !strings.Contains(output, "subagent-2: review beta") || strings.Contains(output, "finding for") {
						t.Fatalf("launch output = %q", output)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "read",
						Name:      "read",
						Arguments: []byte(`{"path":"AGENTS.md"}`),
					}}}, nil
				case 3:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "read" || !strings.Contains(request.Inputs[0].Text, projectInstructions) {
						t.Fatalf("independent continuation inputs = %+v", request.Inputs)
					}
					close(releaseChildren)
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "wait",
						Name:      "subagent_wait",
						Arguments: []byte(`{}`),
					}}}, nil
				case 4:
					if len(request.Inputs) != 2 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "subagent_wait" || !strings.Contains(request.Inputs[0].Text, "completion is available") || request.Inputs[1].Kind != agent.InputInbox || !strings.Contains(request.Inputs[1].Text, "finding for review") {
						t.Fatalf("wait continuation inputs = %+v", request.Inputs)
					}
					if err := sink("combined answer"); err != nil {
						return agent.Response{}, err
					}
					return agent.Response{Text: "combined answer"}, nil
				case 5:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputInbox || !strings.Contains(request.Inputs[0].Text, "finding for review") {
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
			mu.Unlock()
			if len(request.Inputs) != 1 {
				t.Fatalf("child inputs = %+v", request.Inputs)
			}
			<-releaseChildren
			return agent.Response{Text: "finding for " + request.Inputs[0].Text}, nil
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
		if request.Model != "balanced-model" || request.ThinkingLevel != agent.ThinkingLow || !request.FastMode || !strings.Contains(request.Instructions, projectInstructions) || !strings.Contains(request.Instructions, "Current working directory: "+filepath.ToSlash(cwd)) {
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
		tasks = append(tasks, request.Inputs[0].Text)
	}
	slices.Sort(tasks)
	if !slices.Equal(tasks, []string{"review alpha", "review beta"}) {
		t.Fatalf("child tasks = %v", tasks)
	}
}
