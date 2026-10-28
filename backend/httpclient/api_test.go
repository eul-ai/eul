package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAPIErrorCodeAndClassification(t *testing.T) {
	for _, test := range []struct {
		input string
		want  APIErrorCode
	}{
		{input: `{"code":"rate_limit"}`, want: "rate_limit"},
		{input: `{"code":429}`, want: "429"},
		{input: `{"code":null}`},
	} {
		var detail APIError
		if err := json.Unmarshal([]byte(test.input), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Code != test.want {
			t.Fatalf("code for %s = %q, want %q", test.input, detail.Code, test.want)
		}
	}
	if !retryableAPIError(APIError{Code: "429"}) || !retryableAPIError(APIError{Type: "overloaded_error"}) {
		t.Fatal("transient API error was not classified as retryable")
	}
	if !contextLimitAPIError(APIError{Type: "invalid_request_error", Message: "maximum context exceeded"}) {
		t.Fatal("context limit API error was not classified")
	}
	if contextLimitAPIError(APIError{Type: "invalid_request_error", Message: "max_tokens is too large: too many tokens requested"}) {
		t.Fatal("output token error was classified as an input context limit")
	}
}

func TestFormatAPIErrorIncludesProviderDetail(t *testing.T) {
	detail := APIError{Code: "400", Message: "Provider returned error", Metadata: json.RawMessage(`{"provider_name":"Google AI Studio","raw":"{\"message\":\"Corrupted thought signature\"}"}`)}
	if formatted := FormatAPIError(detail); !strings.Contains(formatted, "Google AI Studio") || !strings.Contains(formatted, "Corrupted thought signature") {
		t.Fatalf("formatted = %q", formatted)
	}
}

func TestAPIErrorConfigDecodesResponseAndRetryHint(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader(`{"error":{"type":"rate_limit_error","code":429,"message":"slow down","metadata":{"provider":"test"}}}`)}
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header:     http.Header{"Retry-After": []string{"2"}},
			Body:       body,
		}, nil
	})}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = APIErrorConfig{Prefix: "provider"}.Do(context.Background(), client, request, "request")
	var responseErr *APIResponseError
	if !errors.As(err, &responseErr) || responseErr.statusCode != http.StatusTooManyRequests || responseErr.retryAfter != 2*time.Second || responseErr.detail.Code != "429" || string(responseErr.detail.Metadata) != `{"provider":"test"}` || !body.closed {
		t.Fatalf("response error = %#v, closed=%t", err, body.closed)
	}

	if delay, retry := RetryGeneration(err, 1); !retry || delay != 2*time.Second {
		t.Fatalf("retry = %t after %s", retry, delay)
	}
	if _, retry := RetryGeneration(err, maxGenerationAttempts); retry {
		t.Fatal("retry policy exceeded its attempt limit")
	}
}

func TestRetryGenerationAllowsExtendedRecovery(t *testing.T) {
	transient := APIErrorConfig{}.retryableWrapf(errors.New("temporary"), "temporary")
	delay, retry := RetryGeneration(transient, maxGenerationAttempts-1)
	if !retry {
		t.Fatal("retry policy stopped before the final attempt")
	}
	if delay < maxRetryDelay*3/4 {
		t.Fatalf("final retry delay = %s", delay)
	}
}

func TestRetryGenerationClassifiesStreamErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		err         error
		wantRetry   bool
		wantContext bool
	}{
		{name: "incomplete stream", err: ErrIncompleteStream, wantRetry: true},
		{name: "context window", err: ErrContextLimit, wantContext: true},
		{name: "server failure", err: NewFailureError("failed", APIError{Type: "server_error"}), wantRetry: true},
		{name: "overloaded failure", err: NewFailureError("failed", APIError{Type: "api_error"}), wantRetry: true},
		{name: "context failure", err: NewFailureError("failed", APIError{Code: "context_length_exceeded"}), wantContext: true},
		{name: "permanent failure", err: NewFailureError("failed", APIError{Type: "invalid_request_error", Message: "bad request"})},
		{name: "observer", err: deliver("deliver text", ErrIncompleteStream)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, retry := RetryGeneration(test.err, 1); retry != test.wantRetry {
				t.Fatalf("RetryGeneration() = %t, want %t", retry, test.wantRetry)
			}
			if got := IsContextLimit(test.err); got != test.wantContext {
				t.Fatalf("IsContextLimit() = %t, want %t", got, test.wantContext)
			}
		})
	}
}

func TestCompleteJSONSSEOwnsRequestLifecycle(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("stream")}
	var requests int
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization header = %q", request.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})}
	config := JSONSSEConfig{
		HTTPClient: client,
		Endpoint:   "https://example.test/responses",
		PrepareRequest: func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer token")
			return nil
		},
	}

	result, err := CompleteJSONSSE(context.Background(), config, map[string]string{"model": "test"}, "request", func(reader io.Reader) (string, error) {
		data, err := io.ReadAll(reader)
		return string(data), err
	})
	if err != nil || result != "stream" || requests != 1 || !body.closed {
		t.Fatalf("result=%q requests=%d closed=%t err=%v", result, requests, body.closed, err)
	}
}

func TestCompleteJSONSSECancelsIdleResponseOnly(t *testing.T) {
	previous := idleTimeout
	idleTimeout = 100 * time.Millisecond
	defer func() { idleTimeout = previous }()

	const chunks = 5
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			for range chunks {
				time.Sleep(idleTimeout * 2 / 5)
				_, _ = io.WriteString(writer, "x")
			}
			<-request.Context().Done()
			writer.CloseWithError(request.Context().Err())
		}()
		return &http.Response{StatusCode: http.StatusOK, Body: reader}, nil
	})}

	received := 0
	_, err := CompleteJSONSSE(context.Background(), JSONSSEConfig{HTTPClient: client, Endpoint: "https://example.test"}, struct{}{}, "request", func(reader io.Reader) (struct{}, error) {
		data, err := io.ReadAll(reader)
		received = len(data)
		return struct{}{}, err
	})
	if received != chunks || !errors.Is(err, errIdleTimeout) {
		t.Fatalf("received=%d error=%v", received, err)
	}
	if _, retry := RetryGeneration(err, 1); !retry {
		t.Fatal("idle timeout is not retryable")
	}
}

func TestDecodeBoundedJSONAndText(t *testing.T) {
	var decoded map[string]int
	truncated, err := decodeBoundedJSON(strings.NewReader(`{"value":1}`), 64, &decoded)
	if err != nil || truncated || decoded["value"] != 1 {
		t.Fatalf("decoded=%v truncated=%t err=%v", decoded, truncated, err)
	}
	truncated, err = decodeBoundedJSON(strings.NewReader(`{"value":1}`), 4, &decoded)
	if err != nil || !truncated {
		t.Fatalf("truncated=%t err=%v", truncated, err)
	}
	text, truncated, err := readBoundedText(strings.NewReader(" value \n"), 16)
	if err != nil || truncated || text != "value" {
		t.Fatalf("text=%q truncated=%t err=%v", text, truncated, err)
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (body *trackingReadCloser) Close() error {
	body.closed = true
	return nil
}
