package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

func TestDriverRequiresAPIKey(t *testing.T) {
	driver := New()
	driver.getenv = func(string) string { return " " }
	if _, err := driver.Open(backend.Options{}); err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestRuntimeChecksCredentialsLoadsModelsAndCreatesProvider(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("HTTP-Referer") == "" || request.Header.Get("X-Title") != "Eul" {
			t.Errorf("headers = %v", request.Header)
		}
		switch request.URL.Path {
		case "/auth/key":
			if request.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("authorization = %q", request.Header.Get("Authorization"))
			}
			fmt.Fprint(writer, `{"data":{"label":"test"}}`)
		case "/models":
			if request.Header.Get("Authorization") != "" {
				t.Errorf("public model request authorization = %q", request.Header.Get("Authorization"))
			}
			fmt.Fprint(writer, `{"data":[{"id":"vendor/reasoning","context_length":128000,"supported_parameters":["tools","reasoning"]},{"id":"vendor/plain","context_length":32000,"supported_parameters":["tools"]}]}`)
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
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}

	reasoning := configured.ModelMetadata("vendor/reasoning")
	if reasoning.ContextWindow != 128000 || !slices.Equal(reasoning.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh, agent.ThinkingXHigh}) || reasoning.FastMode {
		t.Fatalf("reasoning metadata = %+v", reasoning)
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
	if _, ok := provider.(*client); !ok {
		t.Fatalf("provider = %T", provider)
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
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/auth/key":
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
	opened, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	configured := opened.(*runtime)
	if err := configured.CheckCredentials(context.Background()); err != nil {
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

func TestCredentialCheckReportsInvalidKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
	backendRuntime, err := driver.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = backendRuntime.(backend.CredentialChecker).CheckCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate API key") || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("CheckCredentials() error = %v", err)
	}
}
