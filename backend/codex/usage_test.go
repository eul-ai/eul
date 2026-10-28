package codex

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
)

func TestRuntimeGetsSubscriptionWindows(t *testing.T) {
	const token = "secret-test-token"
	httpClient := handlerClient(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("ChatGPT-Account-Id") != "account" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", request.Header)
		}
		if request.Header.Get("Content-Type") != "" || request.Header.Get("OpenAI-Beta") != "" {
			t.Errorf("GET content headers = %v", request.Header)
		}
		fmt.Fprint(writer, `{"plan_type":"unknown-future-plan","rate_limit":{"allowed":true,"primary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_at":1760000000},"secondary_window":{"used_percent":84,"limit_window_seconds":604800,"reset_at":1760000120}}}`)
	})

	usage, err := loadUsage(context.Background(), testManager(t, token), httpClient)
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

func TestRuntimeHandlesOptionalUsageWindows(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want backend.AccountUsage
	}{
		{name: "no rate limit", body: `{"plan_type":"plus","rate_limit":null}`},
		{name: "one window", body: `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":3600}}}`, want: backend.AccountUsage{Windows: []backend.UsageWindow{{Duration: time.Hour}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpClient := handlerClient(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(writer, test.body)
			})
			usage, err := loadUsage(context.Background(), testManager(t, "token"), httpClient)
			if err != nil || !reflect.DeepEqual(usage, test.want) {
				t.Fatalf("Usage() = %+v, %v; want %+v", usage, err, test.want)
			}
		})
	}
}
