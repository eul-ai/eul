package opencodego

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
)

func TestDriverRequiresAPIKey(t *testing.T) {
	driver := New()
	driver.getenv = func(string) string { return " " }
	if _, err := driver.Open(backend.Options{}); err == nil {
		t.Fatal("Open() accepted an empty API key")
	}
}

func TestRuntimeChecksCredentialsAndLoadsUsage(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method != http.MethodGet || request.URL.Path != "/zen/go/v1/usage" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request = %s %s, headers=%v", request.Method, request.URL.Path, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"usage":{
				"rolling":{"status":"ok","percent":12.4,"resetsAt":"2026-08-16T01:00:00Z"},
				"weekly":{"status":"ok","percent":50.6,"resetsAt":"2026-08-20T01:00:00Z"},
				"monthly":{"status":"rate-limited","percent":101,"resetsAt":"2026-09-01T00:00:00Z"}
			}}`)),
		}, nil
	})}

	driver := New()
	key := "secret"
	driver.getenv = func(string) string { return key }
	driver.baseURL = "https://example.test/zen/go/v1/"
	driver.httpClient = client
	opened, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	key = "changed"
	configured := opened.(*runtime)
	if configured.apiKey != "secret" || configured.baseURL != "https://example.test/zen/go/v1" {
		t.Fatalf("runtime key/base URL = %q %q", configured.apiKey, configured.baseURL)
	}
	if configured.credentialClient.Timeout != credentialHTTPTimeout || configured.generationClient != client {
		t.Fatalf("runtime clients = credential timeout %s, generation %p", configured.credentialClient.Timeout, configured.generationClient)
	}
	if err := configured.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}

	usage, err := configured.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(usage.Windows) != 3 {
		t.Fatalf("requests=%d usage=%+v", requests, usage)
	}
	wantDurations := []time.Duration{5 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}
	wantPercents := []int{12, 51, 100}
	for index, window := range usage.Windows {
		if window.Duration != wantDurations[index] || window.UsedPercent != wantPercents[index] || window.ResetsAt.IsZero() {
			t.Fatalf("usage window %d = %+v", index, window)
		}
	}
	if _, err := configured.NewProvider(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialErrorRedactsAPIKey(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad secret"}}`)),
		}, nil
	})}
	driver := New()
	driver.getenv = func(string) string { return "secret" }
	driver.baseURL = "https://example.test/zen/go/v1"
	driver.httpClient = client
	opened, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = opened.(backend.CredentialChecker).CheckCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("CheckCredentials() error = %v", err)
	}
}
