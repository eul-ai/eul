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

func TestGenerationLimitsUseSharedDefaults(t *testing.T) {
	limits := (GenerationLimits{}).WithDefaults()
	if limits.RequestBytes != DefaultRequestBytes || limits.ResponseBytes != DefaultResponseBytes || limits.ErrorBytes != DefaultErrorResponseBytes {
		t.Fatalf("limits = %+v", limits)
	}

	configured := (GenerationLimits{RequestBytes: 1, ResponseBytes: 2, ErrorBytes: 3}).WithDefaults()
	if configured != (GenerationLimits{RequestBytes: 1, ResponseBytes: 2, ErrorBytes: 3}) {
		t.Fatalf("configured limits = %+v", configured)
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
	request, err := NewJSONSSERequest(ctx, "https://example.test/responses", []byte(`{"input":[]}`))
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
	done, err := ReadSSE(strings.NewReader(stream), int64(len(stream)), func(data []byte) (bool, error) {
		events = append(events, string(data))
		return string(data) == "final", nil
	})
	if err != nil || !done || len(events) != 2 || events[0] != "first\nsecond" || events[1] != "final" {
		t.Fatalf("done=%v events=%q error=%v", done, events, err)
	}

	done, err = ReadSSE(strings.NewReader("data: [DONE]\n\ndata: ignored\n\n"), 1024, func(data []byte) (bool, error) {
		return string(data) == "[DONE]", nil
	})
	if err != nil || !done {
		t.Fatalf("early stop: done=%v error=%v", done, err)
	}

	var crEvents []string
	done, err = ReadSSE(strings.NewReader("data: first\r\rdata: second\r\r"), 1024, func(data []byte) (bool, error) {
		crEvents = append(crEvents, string(data))
		return len(crEvents) == 2, nil
	})
	if err != nil || !done || len(crEvents) != 2 || crEvents[0] != "first" || crEvents[1] != "second" {
		t.Fatalf("CR events: done=%v events=%q error=%v", done, crEvents, err)
	}

	callbackErr := errors.New("callback failed")
	_, err = ReadSSE(strings.NewReader("data: value\n\n"), 1024, func([]byte) (bool, error) {
		return false, callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v", err)
	}

	_, err = ReadSSE(strings.NewReader(stream), int64(len(stream)-1), func([]byte) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("oversized SSE stream was accepted")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{value: "2", want: 2 * time.Second},
		{value: now.Add(3 * time.Second).Format(http.TimeFormat), want: 3 * time.Second},
		{value: now.Add(-time.Second).Format(http.TimeFormat)},
		{value: "invalid"},
	} {
		if got := ParseRetryAfter(test.value, now); got != test.want {
			t.Fatalf("ParseRetryAfter(%q) = %s, want %s", test.value, got, test.want)
		}
	}
}

func TestRetryClassificationAndDelay(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError} {
		if !RetryableHTTPStatus(status) {
			t.Fatalf("status %d is not retryable", status)
		}
	}
	if RetryableHTTPStatus(http.StatusBadRequest) {
		t.Fatal("bad request is retryable")
	}

	for _, err := range []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		syscall.ECONNRESET,
		&net.DNSError{IsTimeout: true},
		http2StreamError{Code: 2},
	} {
		if !RetryableNetworkError(err) {
			t.Fatalf("network error %T is not retryable", err)
		}
	}
	if RetryableNetworkError(errors.New("permanent")) {
		t.Fatal("permanent error is retryable")
	}

	const base = time.Second
	const maximum = 8 * time.Second
	for attempt, nominal := range []time.Duration{base, 2 * base, 4 * base, maximum, maximum} {
		delay := RetryDelay(attempt+1, base, maximum)
		if delay < nominal*3/4 || delay > maximum || nominal < maximum && delay > nominal*5/4 {
			t.Fatalf("RetryDelay(%d) = %s for nominal %s", attempt+1, delay, nominal)
		}
	}
	if delay := RetryDelayWithHint(1, base, maximum, 2*maximum); delay != maximum {
		t.Fatalf("RetryDelayWithHint() = %s", delay)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	classified := ClassifyTransportError(ctx, io.EOF)
	if !classified.ReturnDirectly || !errors.Is(classified.Cause, context.Canceled) {
		t.Fatalf("caller context classification = %+v", classified)
	}
	classified = ClassifyTransportError(context.Background(), context.DeadlineExceeded)
	if classified.ReturnDirectly || !classified.Retryable || !errors.Is(classified.Cause, context.DeadlineExceeded) {
		t.Fatalf("deadline classification = %+v", classified)
	}
	classified = ClassifyTransportError(context.Background(), errors.New("permanent"))
	if classified.ReturnDirectly || classified.Retryable {
		t.Fatalf("permanent classification = %+v", classified)
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := TruncateUTF8("a€b", 3); got != "a" {
		t.Fatalf("TruncateUTF8() = %q", got)
	}
}
