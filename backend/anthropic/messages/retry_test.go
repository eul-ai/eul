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

func TestContextLimitAPIErrorDoesNotClassifyOutputTokenError(t *testing.T) {
	detail := apiError{Type: "invalid_request_error", Message: "max_tokens is too large: too many tokens requested"}
	if contextLimitAPIError(detail) {
		t.Fatal("output token error was classified as an input context limit")
	}
}

func TestGenerateClosesBodyWhenCanceledDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &cancelingReadCloser{cancel: cancel}
	client, err := New(Options{
		Endpoint: "https://example.test/v1/messages",
		RequestOptions: func(agent.Request) (RequestOptions, error) {
			return RequestOptions{MaxTokens: 100}, nil
		},
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       body,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Generate(ctx, agent.Request{Model: "model", Inputs: []agent.Input{agent.NewTextInput("hello")}}, agent.StreamObserver{})
	if err != context.Canceled || !body.closed {
		t.Fatalf("Generate() error = %v, body closed = %v", err, body.closed)
	}
}

type cancelingReadCloser struct {
	cancel context.CancelFunc
	closed bool
}

func (reader *cancelingReadCloser) Read([]byte) (int, error) {
	reader.cancel()
	return 0, context.Canceled
}

func (reader *cancelingReadCloser) Close() error {
	reader.closed = true
	return nil
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
