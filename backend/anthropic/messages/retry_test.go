package messages

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
			name:      "overloaded",
			status:    http.StatusTooManyRequests,
			body:      `{"type":"error","error":{"type":"overloaded_error","message":"slow down secret"}}`,
			wantRetry: true,
		},
		{
			name:        "context limit",
			status:      http.StatusBadRequest,
			body:        `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long secret"}}`,
			wantContext: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(Options{
				Endpoint: "https://example.test/v1/messages",
				Redact:   []string{"secret"},
				RequestOptions: func(agent.Request) (RequestOptions, error) {
					return RequestOptions{MaxTokens: 100}, nil
				},
				HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					header := make(http.Header)
					header.Set("retry-after-ms", "2000")
					return &http.Response{
						StatusCode: test.status,
						Status:     http.StatusText(test.status),
						Header:     header,
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
			delay, retry := client.RetryGeneration(err, 1)
			if retry != test.wantRetry {
				t.Fatalf("RetryGeneration() = %v, want %v", retry, test.wantRetry)
			}
			if retry && delay != 2*time.Second {
				t.Fatalf("retry delay = %s", delay)
			}
			if got := client.IsContextLimitError(err); got != test.wantContext {
				t.Fatalf("IsContextLimitError() = %v, want %v", got, test.wantContext)
			}
		})
	}
}

func TestGenerateHonorsCanceledContext(t *testing.T) {
	client, err := New(Options{Endpoint: "https://example.test/v1/messages"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Generate(ctx, agent.Request{}, agent.StreamObserver{}); err != context.Canceled {
		t.Fatalf("Generate() error = %v", err)
	}
}
