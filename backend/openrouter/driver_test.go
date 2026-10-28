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
	"github.com/eul-ai/eul/backend/protocol"
)

type driverRoundTripperFunc func(*http.Request) (*http.Response, error)

func (function driverRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestOpenRequiresAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", " ")
	if _, err := (driver{baseURL: "https://openrouter.test"}).open(context.Background(), ""); err == nil {
		t.Fatalf("open() error = %v", err)
	}
}

func TestThinkingMetadataUsesCatalogEfforts(t *testing.T) {
	metadata, _ := buildModelMetadata(modelDescription{ID: "model", Reasoning: modelReasoning{
		SupportedEfforts: []string{"high", "low", "unsupported"},
	}})
	levels := metadata.backendMetadata().ThinkingLevels
	if !slices.Equal(levels, []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingHigh}) {
		t.Fatalf("levels = %v", levels)
	}

	metadata, _ = buildModelMetadata(modelDescription{ID: "model", Reasoning: modelReasoning{
		Mandatory:        true,
		SupportedEfforts: []string{"high", "medium", "low"},
	}})
	levels = metadata.backendMetadata().ThinkingLevels
	if !slices.Equal(levels, []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) {
		t.Fatalf("mandatory levels = %v", levels)
	}
}

func TestOpenChecksCredentialsLoadsModelsAndCreatesProvider(t *testing.T) {
	requests := 0
	server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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

	t.Setenv("OPENROUTER_API_KEY", "secret")
	opened, err := (driver{baseURL: "https://openrouter.test", httpClient: server}).open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "changed")
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}

	reasoning, err := opened.Model("vendor/reasoning")
	if err != nil {
		t.Fatal(err)
	}
	if reasoning.ContextWindow != 128000 || !slices.Equal(reasoning.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) || reasoning.FastMode {
		t.Fatalf("reasoning metadata = %+v", reasoning)
	}
	plain, err := opened.Model("vendor/plain")
	if err != nil || plain.ContextWindow != 32000 || !slices.Equal(plain.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("plain metadata = %+v, %v", plain, err)
	}
	if _, err := opened.Model("unknown"); err == nil {
		t.Fatal("unknown model was accepted")
	}

	configuredProvider, ok := opened.Provider.(*protocol.Client)
	if !ok {
		t.Fatalf("provider = %T", opened.Provider)
	}
	state := []byte(`{"version":1,"items":[{"type":"message","role":"assistant","content":"history"}]}`)
	if !configuredProvider.ShouldCompact(agent.Request{Model: "vendor/reasoning", State: state}, agent.Usage{TotalTokens: 115_200}) {
		t.Fatal("provider did not use the model context window for compaction")
	}
	if _, err := opened.Provider.Generate(context.Background(), agent.Request{
		Model: "vendor/plain", ThinkingLevel: agent.ThinkingOff,
	}, agent.StreamObserver{}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountLoadsUsage(t *testing.T) {
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
			server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/key" || request.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("request = %s %s, authorization = %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
				}
				fmt.Fprint(writer, test.body)
			}))

			usage, err := (account{apiKey: "secret", baseURL: "https://openrouter.test", client: server}).usage(context.Background())
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

func TestAccountUsageRejectsTrailingData(t *testing.T) {
	client := &http.Client{Transport: driverRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":{"usage_monthly":12.34}} trailing`)),
		}, nil
	})}
	if _, err := (account{apiKey: "secret", baseURL: "https://example.test", client: client}).usage(context.Background()); err == nil {
		t.Fatal("usage response with trailing data was accepted")
	}
}

func TestOpenRejectsMalformedCredentialResponse(t *testing.T) {
	server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{`)
	}))

	t.Setenv("OPENROUTER_API_KEY", "secret")
	if _, err := (driver{baseURL: "https://openrouter.test", httpClient: server}).open(context.Background(), ""); err == nil {
		t.Fatal("malformed credential response was accepted")
	}
}

func TestOpenReportsInvalidKey(t *testing.T) {
	server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/models" {
			fmt.Fprint(writer, `{"data":[]}`)
			return
		}
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"message":"invalid secret key"}}`)
	}))

	t.Setenv("OPENROUTER_API_KEY", "secret")
	_, err := (driver{baseURL: "https://openrouter.test", httpClient: server}).open(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("open() error = %v", err)
	}
}
