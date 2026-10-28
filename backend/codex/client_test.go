package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/codex/oauth"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func handlerClient(handler http.HandlerFunc) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder.Result(), nil
	})}
}

func writeCredential(t *testing.T, path, accessToken string) {
	t.Helper()
	credential := fmt.Sprintf(`{"access_token":%q,"refresh_token":"refresh","expires_at":%d,"account_id":"account"}`, accessToken, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(path, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testManager(t *testing.T, accessToken string) *oauth.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeCredential(t, path, accessToken)
	return oauth.NewManager(path)
}

func TestClientUsesOAuthEndpointHeadersShapeAndSSE(t *testing.T) {
	const token = "oauth-access-token"
	httpClient := handlerClient(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.String() != "https://chatgpt.com/backend-api/codex/responses" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		for header, want := range map[string]string{
			"Authorization":         "Bearer " + token,
			"chatgpt-account-id":    "account",
			"originator":            "eul",
			"User-Agent":            "eul",
			"OpenAI-Beta":           "responses=experimental",
			"x-codex-beta-features": "remote_compaction_v2",
			"Accept":                "text/event-stream",
			"Content-Type":          "application/json",
		} {
			if got := request.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), token) {
			t.Errorf("request body leaked auth: %s", body)
		}
		var wire struct {
			Input             []json.RawMessage `json:"input"`
			Stream            bool              `json:"stream"`
			Store             bool              `json:"store"`
			ServiceTier       string            `json:"service_tier"`
			Include           []string          `json:"include"`
			ParallelToolCalls bool              `json:"parallel_tool_calls"`
			Text              *struct {
				Verbosity string `json:"verbosity"`
			} `json:"text"`
			Reasoning *struct {
				Effort  string `json:"effort"`
				Summary string `json:"summary"`
			} `json:"reasoning"`
			ToolChoice string `json:"tool_choice"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Error(err)
		}
		if !wire.Stream || wire.Store || wire.ServiceTier != "priority" || len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" || wire.Text == nil || wire.Text.Verbosity != "low" || wire.Reasoning == nil || wire.Reasoning.Effort != "xhigh" || wire.Reasoning.Summary != "auto" || wire.ToolChoice != "auto" || !wire.ParallelToolCalls || len(wire.Input) != 2 {
			t.Errorf("Codex request shape = %+v", wire)
		}
		var inbox struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(wire.Input[1], &inbox); err != nil || inbox.Type != "agent_message" {
			t.Errorf("Codex inbox input = %s, error = %v", wire.Input[1], err)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Assessing request\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_part.done\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"subscription answer\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"subscription answer\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
	})

	var delivered, reasoning string
	response, err := newProvider(testManager(t, token), httpClient).Generate(context.Background(), agent.Request{
		Model:         modelMain,
		ThinkingLevel: agent.ThinkingXHigh,
		FastMode:      true,
		Inputs: []agent.Input{
			agent.NewTextInput("hello"),
			agent.NewInboxInput("subagent result"),
		},
		Tools: []agent.ToolDefinition{{Name: "read", Parameters: agent.JSONSchema{Type: "object"}}},
	}, agent.StreamObserver{
		Text:      func(text string) error { delivered += text; return nil },
		Reasoning: func(text string) error { reasoning += text; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "subscription answer" || delivered != response.Text || reasoning != "Assessing request\n\n" || response.Usage.TotalTokens != 5 {
		t.Fatalf("response=%+v delivered=%q reasoning=%q", response, delivered, reasoning)
	}
}

func TestClientResolvesCredentialPerRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeCredential(t, path, "first")

	var authorization string
	httpClient := handlerClient(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	})
	client := newProvider(oauth.NewManager(path), httpClient)
	request := agent.Request{Model: modelMain, ThinkingLevel: agent.ThinkingMedium, Inputs: []agent.Input{agent.NewTextInput("hello")}}
	if _, err := client.Generate(context.Background(), request, agent.StreamObserver{}); err != nil || authorization != "Bearer first" {
		t.Fatalf("first authorization = %q, error = %v", authorization, err)
	}

	writeCredential(t, path, "second")
	if _, err := client.Generate(context.Background(), request, agent.StreamObserver{}); err != nil || authorization != "Bearer second" {
		t.Fatalf("second authorization = %q, error = %v", authorization, err)
	}
}

func TestClientCredentialErrorsPropagateWithoutRequest(t *testing.T) {
	transportFailure := errors.New("unexpected request")
	httpClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportFailure
	})}
	manager := oauth.NewManager(filepath.Join(t.TempDir(), "auth.json"))
	_, err := newProvider(manager, httpClient).Generate(context.Background(), agent.Request{Model: modelMain, ThinkingLevel: agent.ThinkingMedium}, agent.StreamObserver{})
	if err == nil || errors.Is(err, transportFailure) {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestClientCompactsAndReplaysSharedState(t *testing.T) {
	calls := 0
	httpClient := handlerClient(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var wire struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			if len(wire.Input) != 2 {
				t.Errorf("compact input = %s", wire.Input)
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		case 2:
			if len(wire.Input) != 3 {
				t.Errorf("replayed input = %s", wire.Input)
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		}
	})

	client := newProvider(testManager(t, "token"), httpClient)
	compacted, err := client.Compact(context.Background(), agent.Request{
		Model: modelMain, ThinkingLevel: agent.ThinkingMedium, Inputs: []agent.Input{agent.NewTextInput("compact me")},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), agent.Request{
		Model: modelMain, ThinkingLevel: agent.ThinkingMedium, State: compacted.State, Inputs: []agent.Input{agent.NewTextInput("continue")},
	}, agent.StreamObserver{})
	if err != nil || response.Text != "done" || calls != 2 {
		t.Fatalf("response=%+v error=%v calls=%d", response, err, calls)
	}
}

func TestClientShouldCompact(t *testing.T) {
	client := newProvider(nil, nil)
	state := []byte(`{"version":1}`)
	limit := models[modelMain].contextWindow * 9 / 10
	tests := []struct {
		name    string
		request agent.Request
		usage   agent.Usage
		want    bool
	}{
		{name: "no state", request: agent.Request{Model: modelMain}, usage: agent.Usage{TotalTokens: limit}, want: false},
		{name: "no usage", request: agent.Request{Model: modelMain, State: state}, want: false},
		{name: "unknown model", request: agent.Request{Model: "unknown", State: state}, usage: agent.Usage{TotalTokens: limit}, want: false},
		{name: "below limit", request: agent.Request{Model: modelMain, State: state}, usage: agent.Usage{TotalTokens: limit - 1}, want: false},
		{name: "at limit", request: agent.Request{Model: modelBalanced, State: state}, usage: agent.Usage{TotalTokens: limit}, want: true},
		{name: "pending input crosses limit", request: agent.Request{Model: modelFast, State: state, Inputs: []agent.Input{agent.NewTextInput("12345678")}}, usage: agent.Usage{TotalTokens: limit - 2}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := client.ShouldCompact(test.request, test.usage); got != test.want {
				t.Fatalf("ShouldCompact() = %v, want %v", got, test.want)
			}
		})
	}
}
