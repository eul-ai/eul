package chatcompletions

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestHTTPErrorRedactionRetryAndContextClassification(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantRetry   bool
		wantContext bool
	}{
		{
			name:      "rate limit",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"type":"rate_limit_error","message":"slow down secret"}}`,
			wantRetry: true,
		},
		{
			name:        "context limit",
			status:      http.StatusBadRequest,
			body:        `{"error":{"code":"context_length_exceeded","message":"too long secret"}}`,
			wantContext: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(Options{
				Endpoint: "https://example.test/v1/chat/completions",
				Redact:   []string{"secret"},
				HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: test.status,
						Status:     http.StatusText(test.status),
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(test.body)),
					}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Generate(context.Background(), agent.Request{
				Model:  "model",
				Inputs: []agent.Input{agent.NewTextInput("hello")},
			}, agent.StreamObserver{})
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("Generate() error = %v", err)
			}
			if _, retry := client.RetryGeneration(err, 1); retry != test.wantRetry {
				t.Fatalf("RetryGeneration() = %v, want %v", retry, test.wantRetry)
			}
			if got := client.IsContextLimitError(err); got != test.wantContext {
				t.Fatalf("IsContextLimitError() = %v, want %v", got, test.wantContext)
			}
		})
	}
}

func TestGenerateHonorsCanceledContext(t *testing.T) {
	client, err := New(Options{Endpoint: "https://example.test/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Generate(ctx, agent.Request{}, agent.StreamObserver{}); err != context.Canceled {
		t.Fatalf("Generate() error = %v", err)
	}
}
