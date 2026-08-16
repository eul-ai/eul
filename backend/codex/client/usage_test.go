package client

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/testhttp"
)

func TestUsageClientGetsSubscriptionWindows(t *testing.T) {
	const token = "secret-test-token"
	server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/codex/usage" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-Id") != "account" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", request.Header)
		}
		if request.Header.Get("Content-Type") != "" || request.Header.Get("OpenAI-Beta") != "" {
			t.Errorf("GET content headers = %v", request.Header)
		}
		writeUsageJSON(t, writer, map[string]any{
			"plan_type": "unknown-future-plan",
			"rate_limit": map[string]any{
				"allowed":       true,
				"limit_reached": false,
				"primary_window": map[string]any{
					"used_percent": 42, "limit_window_seconds": 5 * 60 * 60,
					"reset_after_seconds": 120, "reset_at": 1_760_000_000,
				},
				"secondary_window": map[string]any{
					"used_percent": 84, "limit_window_seconds": 7 * 24 * 60 * 60,
					"reset_after_seconds": 240, "reset_at": 1_760_000_120,
				},
			},
		})
	}))
	defer server.Close()

	client := newTestUsageClient(t, token, server)
	usage, err := client.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := backend.AccountUsage{Windows: []backend.UsageWindow{
		{Duration: 5 * time.Hour, UsedPercent: 42, ResetsAt: time.Unix(1_760_000_000, 0)},
		{Duration: 7 * 24 * time.Hour, UsedPercent: 84, ResetsAt: time.Unix(1_760_000_120, 0)},
	}}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("Usage() = %+v, want %+v", usage, want)
	}
}

func TestUsageClientHandlesOptionalAndInvalidWindows(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    backend.AccountUsage
		wantErr bool
	}{
		{name: "no rate limit", body: `{"plan_type":"plus","rate_limit":null}`},
		{name: "one window", body: `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":3600}}}`, want: backend.AccountUsage{Windows: []backend.UsageWindow{{Duration: time.Hour}}}},
		{name: "negative percent", body: `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":3600}}}`, wantErr: true},
		{name: "percent over one hundred", body: `{"rate_limit":{"primary_window":{"used_percent":101,"limit_window_seconds":3600}}}`, wantErr: true},
		{name: "zero duration", body: `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":0}}}`, wantErr: true},
		{name: "negative reset", body: `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":3600,"reset_at":-1}}}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			usage, err := newTestUsageClient(t, "token", server).Usage(context.Background())
			if test.wantErr {
				if err == nil {
					t.Fatalf("Usage() error = %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(usage, test.want) {
				t.Fatalf("Usage() = %+v, %v; want %+v", usage, err, test.want)
			}
		})
	}
}

func TestUsageClientEndpointFollowsBaseURLStyle(t *testing.T) {
	chatGPT, err := NewUsage(testTokenSource("token"), UsageOptions{BaseURL: "https://chatgpt.com/backend-api/"})
	if err != nil {
		t.Fatal(err)
	}
	codexAPI, err := NewUsage(testTokenSource("token"), UsageOptions{BaseURL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	hostCollision, err := NewUsage(testTokenSource("token"), UsageOptions{BaseURL: "https://backend-api.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if chatGPT.usageEndpoint != "https://chatgpt.com/backend-api/wham/usage" ||
		codexAPI.usageEndpoint != "https://example.com/api/codex/usage" ||
		hostCollision.usageEndpoint != "https://backend-api.example.com/api/codex/usage" {
		t.Fatalf("usage endpoints = %q, %q, %q", chatGPT.usageEndpoint, codexAPI.usageEndpoint, hostCollision.usageEndpoint)
	}
}

func newTestUsageClient(t *testing.T, token string, server *testhttp.Server) *UsageClient {
	t.Helper()
	client, err := NewUsage(testTokenSource(token), UsageOptions{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeUsageJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}
