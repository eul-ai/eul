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
	if !IsRetryableAPIError(APIError{Code: "429"}) || !IsRetryableAPIError(APIError{Type: "overloaded_error"}) {
		t.Fatal("transient API error was not classified as retryable")
	}
	if !IsContextLimitAPIError(APIError{Type: "invalid_request_error", Message: "maximum context exceeded"}) {
		t.Fatal("context limit API error was not classified")
	}
}

func TestAPIErrorConfigDecodesResponseAndRetryPolicy(t *testing.T) {
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

	formatted := false
	config := APIErrorConfig{
		Prefix:  "provider",
		Maximum: 1024,
		FormatDetail: func(detail APIError) string {
			formatted = true
			return FormatAPIError(detail)
		},
	}
	_, err = config.Do(context.Background(), client, request, "request")
	var responseErr *APIResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode() != http.StatusTooManyRequests || responseErr.RetryAfter() != 2*time.Second || responseErr.Detail().Code != "429" || string(responseErr.Detail().Metadata) != `{"provider":"test"}` || !body.closed || !formatted {
		t.Fatalf("response error = %#v, closed=%t, formatted=%t", err, body.closed, formatted)
	}

	policy := RetryPolicy{MaximumAttempts: 20, BaseDelay: time.Millisecond, MaximumDelay: time.Second}
	if delay, retry := policy.Next(err, 1, true); !retry || delay != time.Second {
		t.Fatalf("retry = %t after %s", retry, delay)
	}
	if _, retry := policy.Next(err, 20, true); retry {
		t.Fatal("retry policy exceeded its attempt limit")
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
		HTTPClient:       client,
		Endpoint:         "https://example.test/responses",
		ErrorConfig:      APIErrorConfig{Maximum: 1024},
		MaxRequestBytes:  1024,
		MaxResponseBytes: 1024,
		PrepareRequest: func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer token")
			return nil
		},
	}

	result, err := CompleteJSONSSE(context.Background(), config, map[string]string{"model": "test"}, "request", func(reader io.Reader, maximum int64) (string, error) {
		data, err := io.ReadAll(reader)
		return string(data), err
	}, IsNonRetryableStreamError)
	if err != nil || result != "stream" || requests != 1 || !body.closed {
		t.Fatalf("result=%q requests=%d closed=%t err=%v", result, requests, body.closed, err)
	}

	config.MaxRequestBytes = 1
	if _, err := CompleteJSONSSE(context.Background(), config, map[string]string{"model": "test"}, "request", func(io.Reader, int64) (string, error) {
		return "", nil
	}, nil); err == nil || requests != 1 {
		t.Fatalf("oversized request error=%v requests=%d", err, requests)
	}
}

func TestDecodeBoundedJSONAndText(t *testing.T) {
	var decoded map[string]int
	truncated, err := DecodeBoundedJSON(strings.NewReader(`{"value":1}`), 64, &decoded)
	if err != nil || truncated || decoded["value"] != 1 {
		t.Fatalf("decoded=%v truncated=%t err=%v", decoded, truncated, err)
	}
	truncated, err = DecodeBoundedJSON(strings.NewReader(`{"value":1}`), 4, &decoded)
	if err != nil || !truncated {
		t.Fatalf("truncated=%t err=%v", truncated, err)
	}
	text, truncated, err := ReadBoundedText(strings.NewReader(" value \n"), 16)
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
