package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

type TokenSourceFunc func(context.Context) (Credential, error)

func (function TokenSourceFunc) Token(ctx context.Context) (Credential, error) {
	return function(ctx)
}

func TestCodexClientUsesOAuthEndpointHeadersShapeAndSSE(t *testing.T) {
	const token = "oauth-access-token"
	const accountID = "account-123"
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/codex/responses" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		for header, want := range map[string]string{
			"Authorization":         "Bearer " + token,
			"chatgpt-account-id":    accountID,
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
		if strings.Contains(string(body), token) || strings.Contains(string(body), accountID) {
			t.Errorf("request body leaked auth: %s", body)
		}
		var wire struct {
			Stream            bool     `json:"stream"`
			Store             bool     `json:"store"`
			ServiceTier       string   `json:"service_tier"`
			Include           []string `json:"include"`
			ParallelToolCalls bool     `json:"parallel_tool_calls"`
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
		if !wire.Stream || wire.Store || wire.ServiceTier != "priority" || len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" || wire.Text == nil || wire.Text.Verbosity != "low" || wire.Reasoning == nil || wire.Reasoning.Effort != "xhigh" || wire.Reasoning.Summary != "auto" || wire.ToolChoice != "auto" || !wire.ParallelToolCalls {
			t.Errorf("Codex request shape = %+v", wire)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Assessing request\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_part.done\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"subscription answer\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"subscription answer\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
	}))
	defer server.Close()

	sourceCalls := 0
	client, err := New(TokenSourceFunc(func(context.Context) (Credential, error) {
		sourceCalls++
		return Credential{AccessToken: token, AccountID: accountID}, nil
	}), Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	var delivered, reasoning string
	response, err := client.Generate(context.Background(), agent.Request{
		Model:         ModelGPT56Sol,
		ThinkingLevel: agent.ThinkingXHigh,
		FastMode:      true,
		Inputs:        []agent.Input{agent.NewTextInput("hello")},
	}, agent.StreamObserver{
		Text:      func(text string) error { delivered += text; return nil },
		Reasoning: func(text string) error { reasoning += text; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "subscription answer" || delivered != response.Text || reasoning != "Assessing request\n\n" || response.Usage.TotalTokens != 5 || sourceCalls != 1 {
		t.Fatalf("response=%+v delivered=%q reasoning=%q sourceCalls=%d", response, delivered, reasoning, sourceCalls)
	}
}

func TestCodexSourceIsResolvedPerRequest(t *testing.T) {
	transportFailure := errors.New("transport sentinel")
	calls := 0
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		want := "first"
		if calls == 2 {
			want = "second"
		}
		if request.Header.Get("Authorization") != "Bearer "+want {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if calls == 2 {
			return nil, transportFailure
		}
		body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	sourceCalls := 0
	client, err := New(TokenSourceFunc(func(context.Context) (Credential, error) {
		sourceCalls++
		return Credential{AccessToken: []string{"first", "second"}[sourceCalls-1], AccountID: "account"}, nil
	}), Options{BaseURL: "https://example.com", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{Model: ModelGPT56Sol, Inputs: []agent.Input{agent.NewTextInput("hello")}}
	if _, err := client.Generate(context.Background(), request, agent.StreamObserver{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(context.Background(), request, agent.StreamObserver{}); !errors.Is(err, transportFailure) {
		t.Fatalf("second Generate() error = %v", err)
	}
	if sourceCalls != 2 {
		t.Fatalf("source calls = %d", sourceCalls)
	}
}

func TestCodexDefaultEndpoint(t *testing.T) {
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://chatgpt.com/backend-api/codex/responses" {
			t.Fatalf("endpoint = %q", request.URL)
		}
		body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	client, err := New(testTokenSource("token"), Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(context.Background(), agent.Request{}, agent.StreamObserver{}); err != nil {
		t.Fatal(err)
	}
}

func TestTokenSourceErrorsPropagateWithoutRequest(t *testing.T) {
	sourceErr := errors.New("refresh unavailable")
	client, err := New(TokenSourceFunc(func(context.Context) (Credential, error) {
		return Credential{}, sourceErr
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), agent.Request{Model: ModelGPT56Sol}, agent.StreamObserver{})
	if err == nil || !errors.Is(err, sourceErr) {
		t.Fatalf("Generate() error = %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
