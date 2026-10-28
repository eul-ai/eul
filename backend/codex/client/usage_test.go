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
		if request.Method != http.MethodGet || request.URL.Path != "/backend-api/wham/usage" {
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

func TestUsageClientHandlesOptionalWindows(t *testing.T) {
	tests := []struct {
		name string
		body string
		want backend.AccountUsage
	}{
		{name: "no rate limit", body: `{"plan_type":"plus","rate_limit":null}`},
		{name: "one window", body: `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":3600}}}`, want: backend.AccountUsage{Windows: []backend.UsageWindow{{Duration: time.Hour}}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			usage, err := newTestUsageClient(t, "token", server).Usage(context.Background())
			if err != nil || !reflect.DeepEqual(usage, test.want) {
				t.Fatalf("Usage() = %+v, %v; want %+v", usage, err, test.want)
			}
		})
	}
}

func newTestUsageClient(t *testing.T, token string, server *testhttp.Server) *UsageClient {
	t.Helper()
	client, err := newUsage(testTokenSource(token), server.Client())
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
