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
	body := &trackingReadCloser{Reader: strings.NewReader(`{"error":{"type":"rate_limit_error","code":429,"message":"slow down"}}`)}
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

	_, err = (APIErrorConfig{Prefix: "provider", Maximum: 1024}).Do(context.Background(), client, request, "request")
	var responseErr *APIResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode() != http.StatusTooManyRequests || responseErr.RetryAfter() != 2*time.Second || responseErr.Detail().Code != "429" || !body.closed {
		t.Fatalf("response error = %#v, closed=%t", err, body.closed)
	}

	policy := RetryPolicy{MaximumAttempts: 20, BaseDelay: time.Millisecond, MaximumDelay: time.Second}
	if delay, retry := policy.Next(err, 1, true); !retry || delay != time.Second {
		t.Fatalf("retry = %t after %s", retry, delay)
	}
	if _, retry := policy.Next(err, 20, true); retry {
		t.Fatal("retry policy exceeded its attempt limit")
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
