package openrouter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/testhttp"
)

type driverRoundTripperFunc func(*http.Request) (*http.Response, error)

func (function driverRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDriverRequiresAPIKey(t *testing.T) {
	driver := New()
	driver.getenv = func(string) string { return " " }
	if _, err := driver.Open(backend.Options{}); err == nil {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestThinkingMetadataUsesCatalogEfforts(t *testing.T) {
	levels, defaultLevel := thinkingMetadata(modelReasoning{
		SupportedEfforts: []string{"high", "low", "unsupported"},
		DefaultEffort:    "low",
	})
	if !slices.Equal(levels, []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh}) || defaultLevel != agent.ThinkingLow {
		t.Fatalf("levels = %v, default = %q", levels, defaultLevel)
	}

	levels, defaultLevel = thinkingMetadata(modelReasoning{
		Mandatory:        true,
		SupportedEfforts: []string{"high", "medium", "low"},
		DefaultEffort:    "medium",
	})
	if !slices.Equal(levels, []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) || defaultLevel != agent.ThinkingMedium {
		t.Fatalf("mandatory levels = %v, default = %q", levels, defaultLevel)
	}
}

func TestRuntimeChecksCredentialsInitializesModelsAndCreatesProvider(t *testing.T) {
	requests := 0
	server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("HTTP-Referer") == "" || request.Header.Get("X-Title") != "Eul" {
			t.Errorf("headers = %v", request.Header)
		}
		switch request.URL.Path {
		case "/key":
			if request.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("authorization = %q", request.Header.Get("Authorization"))
			}
			fmt.Fprint(writer, `{"data":{"label":"test"}}`)
		case "/models":
			if request.Header.Get("Authorization") != "" {
				t.Errorf("public model request authorization = %q", request.Header.Get("Authorization"))
			}
			fmt.Fprint(writer, `{"data":[{"id":"vendor/reasoning","context_length":128000,"reasoning":{"mandatory":true,"supported_efforts":["high","medium","low"],"default_effort":"medium"}},{"id":"vendor/plain","context_length":32000}]}`)
		case "/responses":
			if request.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("generation authorization = %q", request.Header.Get("Authorization"))
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		default:
			t.Errorf("path = %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	driver := New()
	key := "secret"
	driver.getenv = func(string) string { return key }
	driver.baseURL = server.URL
	driver.httpClient = server.Client()
	backendRuntime, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	key = "changed"
	configured := backendRuntime.(*runtime)
	if configured.generationClient.Timeout != 0 || configured.credentialClient.Timeout != credentialHTTPTimeout {
		t.Fatalf("HTTP timeouts: generation=%s credential=%s", configured.generationClient.Timeout, configured.credentialClient.Timeout)
	}
	if err := configured.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := configured.InitializeModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}

	reasoning := configured.ModelMetadata("vendor/reasoning")
	if reasoning.ContextWindow != 128000 || !slices.Equal(reasoning.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) || reasoning.FastMode {
		t.Fatalf("reasoning metadata = %+v", reasoning)
	}
	if configured.modelMetadata("vendor/reasoning").defaultThinkingLevel != agent.ThinkingMedium {
		t.Fatalf("default thinking level = %q", configured.modelMetadata("vendor/reasoning").defaultThinkingLevel)
	}
	plain := configured.ModelMetadata("vendor/plain")
	if plain.ContextWindow != 32000 || !slices.Equal(plain.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("plain metadata = %+v", plain)
	}
	unknown := configured.ModelMetadata("unknown")
	if unknown.ContextWindow != 0 || !slices.Equal(unknown.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("unknown metadata = %+v", unknown)
	}

	provider, err := configured.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	configuredProvider, ok := provider.(*client)
	if !ok {
		t.Fatalf("provider = %T", provider)
	}
	state := []byte(`{"version":1,"items":[{"type":"message","role":"assistant","content":"history"}]}`)
	if !configuredProvider.ShouldCompact(agent.Request{Model: "vendor/reasoning", State: state}, agent.Usage{TotalTokens: 115_200}) {
		t.Fatal("provider did not use cached model context window for compaction")
	}
	if _, err := provider.Generate(context.Background(), agent.Request{
		Model: "vendor/plain", ThinkingLevel: agent.ThinkingOff,
	}, agent.StreamObserver{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := configured.NewProvider()
			done <- err
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent NewProvider(): %v", err)
		}
	}
}

func TestModelCatalogIsCached(t *testing.T) {
	models := 0
	server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/key":
			fmt.Fprint(writer, `{"data":{}}`)
		case "/models":
			models++
			fmt.Fprint(writer, `{"data":[{"id":"vendor/model","context_length":64000,"supported_parameters":[]}]}`)
		}
	}))
	defer server.Close()

	driver := New()
	driver.getenv = func(string) string { return "secret" }
	driver.baseURL = server.URL
	driver.httpClient = server.Client()
	opened, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	configured := opened.(*runtime)
	if err := configured.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := configured.InitializeModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if metadata := configured.ModelMetadata("vendor/model"); metadata.ContextWindow != 64000 {
			t.Fatalf("metadata = %+v", metadata)
		}
	}
	if models != 1 {
		t.Fatalf("model catalog requests = %d", models)
	}
}

func TestRuntimeLoadsAccountUsage(t *testing.T) {
	limitedRemaining := 87.66
	tests := []struct {
		name          string
		body          string
		wantRemaining *float64
	}{
		{name: "limited key", body: `{"data":{"usage_monthly":12.34,"limit_remaining":87.66}}`, wantRemaining: &limitedRemaining},
		{name: "unlimited key", body: `{"data":{"usage_monthly":12.34,"limit_remaining":null}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/key" || request.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("request = %s %s, authorization = %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
				}
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()

			driver := New()
			driver.getenv = func(string) string { return "secret" }
			driver.baseURL = server.URL
			driver.httpClient = server.Client()
			opened, err := driver.Open(backend.Options{})
			if err != nil {
				t.Fatal(err)
			}
			usage, err := opened.(backend.UsageProvider).Usage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if usage.MonthlyUsageUSD == nil || *usage.MonthlyUsageUSD != 12.34 {
				t.Fatalf("monthly usage = %v", usage.MonthlyUsageUSD)
			}
			switch {
			case test.wantRemaining == nil && usage.LimitRemainingUSD != nil:
				t.Fatalf("limit remaining = %v, want nil", *usage.LimitRemainingUSD)
			case test.wantRemaining != nil && (usage.LimitRemainingUSD == nil || *usage.LimitRemainingUSD != *test.wantRemaining):
				t.Fatalf("limit remaining = %v, want %v", usage.LimitRemainingUSD, *test.wantRemaining)
			}
		})
	}
}

func TestRuntimeUsageRejectsTrailingData(t *testing.T) {
	driver := New()
	driver.getenv = func(string) string { return "secret" }
	driver.baseURL = "https://example.test"
	driver.httpClient = &http.Client{Transport: driverRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":{"usage_monthly":12.34}} trailing`)),
		}, nil
	})}
	opened, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.(backend.UsageProvider).Usage(context.Background()); err == nil {
		t.Fatal("usage response with trailing data was accepted")
	}
}

func TestCredentialCheckReportsInvalidKey(t *testing.T) {
	server := testhttp.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/models" {
			fmt.Fprint(writer, `{"data":[]}`)
			return
		}
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"message":"invalid secret key"}}`)
	}))
	defer server.Close()

	driver := New()
	driver.getenv = func(string) string { return "secret" }
	driver.baseURL = server.URL
	driver.httpClient = server.Client()
	backendRuntime, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = backendRuntime.(backend.CredentialChecker).CheckCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("CheckCredentials() error = %v", err)
	}
}
