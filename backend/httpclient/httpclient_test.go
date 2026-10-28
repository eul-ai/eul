package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCloneNoRedirectsCopiesClientDefaults(t *testing.T) {
	source := &http.Client{}
	client := CloneNoRedirects(source, time.Minute)
	if client == source || client.Timeout != time.Minute || source.Timeout != 0 {
		t.Fatalf("source=%+v client=%+v", source, client)
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v", err)
	}
}

func TestCloneNoRedirectsDoesNotApplyZeroTimeout(t *testing.T) {
	source := &http.Client{Timeout: -time.Second}
	client := CloneNoRedirects(source, 0)
	if client.Timeout != -time.Second || source.Timeout != -time.Second {
		t.Fatalf("source=%+v client=%+v", source, client)
	}
}

func TestReadBounded(t *testing.T) {
	data, truncated, err := ReadBounded(strings.NewReader("abcd"), 4)
	if err != nil || truncated || string(data) != "abcd" {
		t.Fatalf("exact read = %q, truncated=%v, error=%v", data, truncated, err)
	}
	data, truncated, err = ReadBounded(strings.NewReader("abcde"), 4)
	if err != nil || !truncated || string(data) != "abcd" {
		t.Fatalf("bounded read = %q, truncated=%v, error=%v", data, truncated, err)
	}
}

func TestNewJSONSSERequest(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "value")
	request, err := newJSONSSERequest(ctx, "https://example.test/responses", []byte(`{"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.String() != "https://example.test/responses" || string(body) != `{"input":[]}` || request.Header.Get("Accept") != "text/event-stream" || request.Header.Get("Content-Type") != "application/json" || request.Context() != ctx {
		t.Fatalf("request = %+v body=%s", request, body)
	}
}

func TestReadSSE(t *testing.T) {
	stream := "event: update\r\ndata: first\r\ndata: second\r\n\r\n: ping\r\ndata: final"
	var events []string
	done, err := ReadSSE(strings.NewReader(stream), func(data []byte) (bool, error) {
		events = append(events, string(data))
		return string(data) == "final", nil
	})
	if err != nil || !done || len(events) != 2 || events[0] != "first\nsecond" || events[1] != "final" {
		t.Fatalf("done=%v events=%q error=%v", done, events, err)
	}

	done, err = ReadSSE(strings.NewReader("data: [DONE]\n\ndata: ignored\n\n"), func(data []byte) (bool, error) {
		return string(data) == "[DONE]", nil
	})
	if err != nil || !done {
		t.Fatalf("early stop: done=%v error=%v", done, err)
	}

	var crEvents []string
	done, err = ReadSSE(strings.NewReader("data: first\r\rdata: second\r\r"), func(data []byte) (bool, error) {
		crEvents = append(crEvents, string(data))
		return len(crEvents) == 2, nil
	})
	if err != nil || !done || len(crEvents) != 2 || crEvents[0] != "first" || crEvents[1] != "second" {
		t.Fatalf("CR events: done=%v events=%q error=%v", done, crEvents, err)
	}

	callbackErr := errors.New("callback failed")
	_, err = ReadSSE(strings.NewReader("data: value\n\n"), func([]byte) (bool, error) {
		return false, callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v", err)
	}

	oversized := "data: " + strings.Repeat("x", int(maxResponseBytes)) + "\n\n"
	_, err = ReadSSE(strings.NewReader(oversized), func([]byte) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("oversized SSE stream was accepted")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		header http.Header
		want   time.Duration
	}{
		{header: http.Header{"Retry-After": {"2"}}, want: 2 * time.Second},
		{header: http.Header{"Retry-After": {now.Add(3 * time.Second).Format(http.TimeFormat)}}, want: 3 * time.Second},
		{header: http.Header{"Retry-After": {now.Add(-time.Second).Format(http.TimeFormat)}}},
		{header: http.Header{"Retry-After": {"invalid"}}},
		{header: http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"2"}}, want: 1500 * time.Millisecond},
	} {
		if got := parseRetryAfter(test.header, now); got != test.want {
			t.Fatalf("parseRetryAfter(%v) = %s, want %s", test.header, got, test.want)
		}
	}
}

func TestRetryClassificationAndDelay(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError} {
		if !retryableHTTPStatus(status) {
			t.Fatalf("status %d is not retryable", status)
		}
	}
	if retryableHTTPStatus(http.StatusBadRequest) {
		t.Fatal("bad request is retryable")
	}

	for _, err := range []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		syscall.ECONNRESET,
		&net.DNSError{IsTimeout: true},
		http2StreamError{Code: 2},
	} {
		if !retryableNetworkError(err) {
			t.Fatalf("network error %T is not retryable", err)
		}
	}
	if retryableNetworkError(errors.New("permanent")) {
		t.Fatal("permanent error is retryable")
	}

	for attempt, nominal := range []time.Duration{baseRetryDelay, 2 * baseRetryDelay, 4 * baseRetryDelay} {
		delay := retryDelay(attempt+1, 0)
		if delay < nominal*3/4 || delay > nominal*5/4 {
			t.Fatalf("retryDelay(%d) = %s for nominal %s", attempt+1, delay, nominal)
		}
	}
	if delay := retryDelay(1, 2*maxRetryDelay); delay != maxRetryDelay {
		t.Fatalf("retryDelay() with hint = %s", delay)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	classified := classifyTransportError(ctx, io.EOF)
	if !classified.returnDirectly || !errors.Is(classified.cause, context.Canceled) {
		t.Fatalf("caller context classification = %+v", classified)
	}
	classified = classifyTransportError(context.Background(), context.DeadlineExceeded)
	if classified.returnDirectly || !classified.retryable || !errors.Is(classified.cause, context.DeadlineExceeded) {
		t.Fatalf("deadline classification = %+v", classified)
	}
	classified = classifyTransportError(context.Background(), errors.New("permanent"))
	if classified.returnDirectly || classified.retryable {
		t.Fatalf("permanent classification = %+v", classified)
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := truncateUTF8("a€b", 3); got != "a" {
		t.Fatalf("truncateUTF8() = %q", got)
	}
}
