package httpclient

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewCopiesClientDefaults(t *testing.T) {
	source := &http.Client{}
	client := New(source, time.Minute)
	if client == source || client.Timeout != time.Minute || source.Timeout != 0 {
		t.Fatalf("source=%+v client=%+v", source, client)
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v", err)
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

func TestRedactAndTruncateUTF8(t *testing.T) {
	if got := Redact("token secret", []string{"secret"}); got != "token [redacted]" {
		t.Fatalf("Redact() = %q", got)
	}
	if got := TruncateUTF8("a€b", 3); got != "a" {
		t.Fatalf("TruncateUTF8() = %q", got)
	}
}
