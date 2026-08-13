package client

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientUsageGetsSubscriptionWindows(t *testing.T) {
	const token = "secret-test-token"
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/codex/usage" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-Id") != "account" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", request.Header)
		}
		if request.Header.Get("Content-Type") != "" || request.Header.Get("OpenAI-Beta") != "" {
			t.Errorf("GET content headers = %v", request.Header)
		}
		writeCompactJSON(t, writer, map[string]any{
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

	client := newTestClient(t, token, server.URL, Options{})
	usage, err := client.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := AccountUsage{Windows: []UsageWindow{
		{Duration: 5 * time.Hour, UsedPercent: 42, ResetsAt: time.Unix(1_760_000_000, 0)},
		{Duration: 7 * 24 * time.Hour, UsedPercent: 84, ResetsAt: time.Unix(1_760_000_120, 0)},
	}}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("Usage() = %+v, want %+v", usage, want)
	}
}

func TestClientUsageHandlesOptionalAndInvalidWindows(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    AccountUsage
		wantErr string
	}{
		{name: "no rate limit", body: `{"plan_type":"plus","rate_limit":null}`},
		{name: "one window", body: `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":3600}}}`, want: AccountUsage{Windows: []UsageWindow{{Duration: time.Hour}}}},
		{name: "negative percent", body: `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":3600}}}`, wantErr: "invalid used percentage"},
		{name: "percent over one hundred", body: `{"rate_limit":{"primary_window":{"used_percent":101,"limit_window_seconds":3600}}}`, wantErr: "invalid used percentage"},
		{name: "zero duration", body: `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":0}}}`, wantErr: "invalid window duration"},
		{name: "negative reset", body: `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":3600,"reset_at":-1}}}`, wantErr: "invalid reset time"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			usage, err := newTestClient(t, "token", server.URL, Options{}).Usage(context.Background())
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
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

func TestClientUsageEndpointFollowsBaseURLStyle(t *testing.T) {
	chatGPT, err := New(testTokenSource("token"), Options{BaseURL: "https://chatgpt.com/backend-api/"})
	if err != nil {
		t.Fatal(err)
	}
	codexAPI, err := New(testTokenSource("token"), Options{BaseURL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if chatGPT.usageEndpoint != "https://chatgpt.com/backend-api/wham/usage" || codexAPI.usageEndpoint != "https://example.com/api/codex/usage" {
		t.Fatalf("usage endpoints = %q, %q", chatGPT.usageEndpoint, codexAPI.usageEndpoint)
	}
}
