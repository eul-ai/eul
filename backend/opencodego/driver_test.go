package opencodego

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
)

const testUsageJSON = `{"usage":{
	"rolling":{"status":"ok","percent":12.4,"resetsAt":"2026-08-16T01:00:00Z"},
	"weekly":{"status":"ok","percent":50.6,"resetsAt":"2026-08-20T01:00:00Z"},
	"monthly":{"status":"rate-limited","percent":101,"resetsAt":"2026-09-01T00:00:00Z"}
}}`

func testCatalogProvider(t *testing.T) catalogProvider {
	t.Helper()
	const document = `{
		"id":"opencode-go",
		"npm":"@ai-sdk/openai-compatible",
		"models":{
			"grok-4.5":{"id":"grok-4.5","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],"limit":{"context":500000,"output":500000},"provider":{"npm":"@ai-sdk/openai"}},
			"gpt-5.6-luna":{"id":"gpt-5.6-luna","reasoning":true,"reasoning_options":[{"type":"effort","values":["none","low","medium","high","xhigh","max"]}],"limit":{"context":1050000,"output":128000},"provider":{"npm":"@ai-sdk/openai"}},
			"glm-5.2":{"id":"glm-5.2","reasoning":true,"reasoning_options":[{"type":"effort","values":["high","max"]}],"limit":{"context":1000000,"output":131072}},
			"hy3":{"id":"hy3","reasoning":true,"reasoning_options":[{"type":"effort","values":["none","low","high"]}],"limit":{"context":256000,"output":64000}},
			"deepseek-v4-pro":{"id":"deepseek-v4-pro","reasoning":true,"reasoning_options":[{"type":"effort","values":["high","max"]}],"limit":{"context":1000000,"output":384000}},
			"kimi-k3":{"id":"kimi-k3","reasoning":true,"reasoning_options":[{"type":"effort","values":["max"]}],"limit":{"context":1048576,"output":131072}},
			"kimi-k2.6":{"id":"kimi-k2.6","reasoning":true,"reasoning_options":[],"limit":{"context":262144,"output":65536}},
			"minimax-m3":{"id":"minimax-m3","reasoning":true,"reasoning_options":[{"type":"toggle"}],"limit":{"context":1000000,"output":131072},"provider":{"npm":"@ai-sdk/anthropic"}},
			"qwen3.8-max":{"id":"qwen3.8-max","reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"budget_tokens","max":262144}],"limit":{"context":1000000,"output":131072},"provider":{"npm":"@ai-sdk/anthropic"}},
			"public-only":{"id":"public-only","reasoning":false,"reasoning_options":[],"limit":{"context":123456,"output":32768}}
		}
	}`
	var provider catalogProvider
	if err := json.Unmarshal([]byte(document), &provider); err != nil {
		t.Fatal(err)
	}
	return provider
}

func testCatalogJSON(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]catalogProvider{ID: testCatalogProvider(t)})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func testLiveModelsJSON(ids ...string) string {
	data := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]string{"id": id, "object": "model"})
	}
	body, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	return string(body)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDriverRequiresAPIKey(t *testing.T) {
	driver := New()
	driver.getenv = func(string) string { return " " }
	if _, err := driver.Open(backend.Options{}); err == nil {
		t.Fatal("Open() accepted an empty API key")
	}
}

func TestRuntimeValidatesModels(t *testing.T) {
	configured := &runtime{models: testModelInfos()}
	if err := configured.ValidateModel("qwen3.8-max"); err != nil {
		t.Fatal(err)
	}
	if err := configured.ValidateModel("unknown"); err == nil {
		t.Fatal("unknown model was accepted")
	}
}

func TestRuntimeChecksCredentialsLoadsModelsAndUsage(t *testing.T) {
	var usageRequests, liveRequests, catalogRequests int
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Host == "catalog.test":
			catalogRequests++
			if request.Method != http.MethodGet || request.URL.Path != "/api.json" || request.Header.Get("Authorization") != "" {
				t.Errorf("catalog request = %s %s, headers=%v", request.Method, request.URL, request.Header)
			}
			response := testHTTPResponse(http.StatusOK, testCatalogJSON(t))
			response.Header.Set("ETag", `"catalog-v1"`)
			return response, nil
		case request.URL.Path == "/zen/go/v1/usage":
			usageRequests++
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("usage request = %s %s, headers=%v", request.Method, request.URL.Path, request.Header)
			}
			return testHTTPResponse(http.StatusOK, testUsageJSON), nil
		case request.URL.Path == "/zen/go/v1/models":
			liveRequests++
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("models request = %s %s, headers=%v", request.Method, request.URL.Path, request.Header)
			}
			return testHTTPResponse(http.StatusOK, testLiveModelsJSON("grok-4.5", "glm-5.2", "minimax-m3", "live-only")), nil
		default:
			t.Fatalf("unexpected request: %s", request.URL)
			return nil, nil
		}
	})}

	driver := New()
	key := "secret"
	driver.getenv = func(string) string { return key }
	driver.baseURL = "https://example.test/zen/go/v1/"
	driver.catalogURL = "https://catalog.test/api.json"
	driver.httpClient = client
	home := t.TempDir()
	opened, err := driver.Open(backend.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	key = "changed"
	configured := opened.(*runtime)
	if configured.apiKey != "secret" || configured.baseURL != "https://example.test/zen/go/v1" || configured.catalogCachePath != filepath.Join(home, "cache", "opencode-go-models.json") {
		t.Fatalf("runtime key/base URL/cache path = %q %q %q", configured.apiKey, configured.baseURL, configured.catalogCachePath)
	}
	if configured.credentialClient.Timeout != credentialHTTPTimeout || configured.generationClient != client {
		t.Fatalf("runtime clients = credential timeout %s, generation %p", configured.credentialClient.Timeout, configured.generationClient)
	}
	if err := configured.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := configured.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"grok-4.5", "glm-5.2", "minimax-m3"} {
		if err := configured.ValidateModel(model); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range []string{"public-only", "live-only"} {
		if err := configured.ValidateModel(model); err == nil {
			t.Fatalf("model %q was accepted", model)
		}
	}

	usage, err := configured.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usageRequests != 3 || liveRequests != 2 || catalogRequests != 1 || len(usage.Windows) != 3 {
		t.Fatalf("requests usage=%d live=%d catalog=%d usage=%+v", usageRequests, liveRequests, catalogRequests, usage)
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

func TestLoadUsageRejectsTrailingData(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return testHTTPResponse(http.StatusOK, `{"usage":{}} trailing`), nil
	})}
	driver := New()
	driver.getenv = func(string) string { return "secret" }
	driver.baseURL = "https://example.test/zen/go/v1"
	driver.httpClient = client
	opened, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.(*runtime).loadUsage(context.Background()); err == nil {
		t.Fatal("usage response with trailing data was accepted")
	}
}

func TestCredentialErrorRedactsAPIKey(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return testHTTPResponse(http.StatusUnauthorized, `{"error":{"message":"bad secret"}}`), nil
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
