package responses

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestClientBoundsHTTPErrors(t *testing.T) {
	const key = "top-secret-key"
	server := responseServer(t, http.StatusBadRequest, strings.Repeat(key+" ", 100))
	defer server.Close()
	client := newTestClient(t, key, server.URL, Options{})
	client.maxErrorBytes = 160
	_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
	if err == nil {
		t.Fatal("Generate() succeeded")
	}
	var responseErr *httpResponseError
	if !errors.As(err, &responseErr) || responseErr.statusCode != http.StatusBadRequest || len(err.Error()) > 160 {
		t.Fatalf("bounded error = %q (%d bytes)", err, len(err.Error()))
	}
}

func TestClientParsesStructuredHTTPError(t *testing.T) {
	server := responseServer(t, http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","code":"rate_limit","message":"slow down"}}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
	var responseErr *httpResponseError
	if !errors.As(err, &responseErr) || responseErr.statusCode != http.StatusTooManyRequests || responseErr.detail.Type != "rate_limit_error" || responseErr.detail.Code != "rate_limit" || responseErr.detail.Message != "slow down" {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestClientClassifiesContextLimitErrorsForCompaction(t *testing.T) {
	t.Run("HTTP error", func(t *testing.T) {
		server := responseServer(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too many tokens"}}`)
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})

		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if err == nil || !client.IsContextLimitError(err) {
			t.Fatalf("Generate() error = %v, should compact = %t", err, client.IsContextLimitError(err))
		}
		if _, retry := client.RetryGeneration(err, 1); retry {
			t.Fatal("context limit error was classified as a transient retry")
		}
	})

	t.Run("SSE error", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"too many tokens\"}}\n\n")
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})

		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if err == nil || !client.IsContextLimitError(err) {
			t.Fatalf("Generate() error = %v, should compact = %t", err, client.IsContextLimitError(err))
		}
	})

	t.Run("terminal response error", func(t *testing.T) {
		server := responseServer(t, http.StatusOK, `{"status":"failed","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too many tokens"},"output":[]}`)
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})

		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if err == nil || !client.IsContextLimitError(err) {
			t.Fatalf("Generate() error = %v, should compact = %t", err, client.IsContextLimitError(err))
		}
	})
}

func TestClientRetriesTransientGenerationThroughEngine(t *testing.T) {
	calls := 0
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			fmt.Fprint(writer, "data: {\"type\":\"error\",\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"overloaded\"}}\n\n")
			return
		}
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"recovered\"}]}]}}\n\n")
	}))
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	engine := agent.New(client, emptyToolbox{}, agent.Options{Model: "test-model"})

	var retries []agent.Event
	result, err := engine.Run(context.Background(), "hello", func(event agent.Event) error {
		if event.Kind == agent.EventGenerationRetry {
			retries = append(retries, event)
		}
		return nil
	})
	if err != nil || result.Text != "recovered" || calls != 2 {
		t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
	}
	if len(retries) != 1 || retries[0].Attempt != 2 {
		t.Fatalf("retry events = %+v", retries)
	}
}

func TestClientRetriesHTTP2InternalStreamErrorThroughEngine(t *testing.T) {
	var calls atomic.Int32
	server := newUnstartedTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor != 2 {
			t.Errorf("request protocol = %s", request.Proto)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			panic("reset stream")
		}
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"recovered\"}]}]}}\n\n")
	}))
	server.EnableHTTP2 = true
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	client := newTestClient(t, "key", server.URL, Options{HTTPClient: server.Client()})
	engine := agent.New(client, emptyToolbox{}, agent.Options{Model: "test-model"})

	result, err := engine.Run(context.Background(), "hello", func(agent.Event) error { return nil })
	if err != nil || result.Text != "recovered" || calls.Load() != 2 {
		t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls.Load())
	}
}

func TestClientClassifiesTransientGenerationErrorsForRetry(t *testing.T) {
	t.Run("SSE server error", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"failed\"}}\n\n")
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})

		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		delay, retry := client.RetryGeneration(err, 1)
		if err == nil || !retry || delay <= 0 {
			t.Fatalf("Generate() error = %v, delay = %s, retry = %t", err, delay, retry)
		}
	})

	t.Run("SSE overloaded code", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"error\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"overloaded\"}}\n\n")
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})

		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if _, retry := client.RetryGeneration(err, 1); err == nil || !retry {
			t.Fatalf("Generate() error = %v, retry = %t", err, retry)
		}
	})

	t.Run("connection reset", func(t *testing.T) {
		transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       errorReadCloser{err: syscall.ECONNRESET},
			}, nil
		})
		client, err := New(Options{
			PrepareRequest: testPrepareRequest("key"), RequestOptions: testRequestOptions,
			Endpoint:   "https://example.com/responses",
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatal(err)
		}

		_, err = generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if _, retry := client.RetryGeneration(err, 1); err == nil || !retry {
			t.Fatalf("Generate() error = %v, retry = %t", err, retry)
		}
	})

	t.Run("Retry-After is bounded", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Retry-After", "600")
			writer.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(writer, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})

		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		delay, retry := client.RetryGeneration(err, 1)
		if err == nil || !retry || delay != generationRetryMaxDelay {
			t.Fatalf("Generate() error = %v, delay = %s, retry = %t", err, delay, retry)
		}
	})
}

func TestGenerationRetryAllowsExtendedRecovery(t *testing.T) {
	transient := &retryableOperationError{cause: errors.New("temporary")}
	if _, retry := (&Client{}).RetryGeneration(transient, maximumGenerationAttempts-1); !retry {
		t.Fatal("retry policy stopped before the final attempt")
	}
	if delay := generationRetryDelay(maximumGenerationAttempts - 1); delay < generationRetryMaxDelay*3/4 {
		t.Fatalf("final retry delay = %s", delay)
	}
}

func TestClientDoesNotRetryPermanentOrObserverErrors(t *testing.T) {
	server := responseServer(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"bad request"}}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
	if _, retry := client.RetryGeneration(err, 1); err == nil || retry {
		t.Fatalf("HTTP error = %v, retry = %t", err, retry)
	}
	if client.IsContextLimitError(err) {
		t.Fatalf("permanent HTTP error was classified for compaction: %v", err)
	}

	server = responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
	defer server.Close()
	client = newTestClient(t, "key", server.URL, Options{})
	sinkErr := &net.DNSError{IsTimeout: true, Err: "sink timeout", Name: "sink"}
	_, err = generate(client, context.Background(), baseRequest(), func(string) error { return sinkErr }, nil, nil)
	if _, retry := client.RetryGeneration(err, 1); !errors.Is(err, sinkErr) || retry {
		t.Fatalf("observer error = %v, retry = %t", err, retry)
	}

	transient := &retryableOperationError{cause: errors.New("temporary")}
	if _, retry := client.RetryGeneration(transient, maximumGenerationAttempts); retry {
		t.Fatal("retry policy exceeded maximum attempts")
	}
}
