package httpclient

import (
	"net/http"
	"time"
)

const (
	DefaultGenerationHTTPTimeout       = 10 * time.Minute
	DefaultRequestBytes          int64 = 32 * 1024 * 1024
	DefaultResponseBytes         int64 = 16 * 1024 * 1024
	DefaultErrorResponseBytes    int64 = 64 * 1024
)

type GenerationLimits struct {
	RequestBytes  int64
	ResponseBytes int64
	ErrorBytes    int64
}

func (limits GenerationLimits) WithDefaults() GenerationLimits {
	if limits.RequestBytes <= 0 {
		limits.RequestBytes = DefaultRequestBytes
	}
	if limits.ResponseBytes <= 0 {
		limits.ResponseBytes = DefaultResponseBytes
	}
	if limits.ErrorBytes <= 0 {
		limits.ErrorBytes = DefaultErrorResponseBytes
	}
	return limits
}

func CloneNoRedirects(source *http.Client, timeout time.Duration) *http.Client {
	if source == nil {
		source = http.DefaultClient
	}
	cloned := *source
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if timeout > 0 && cloned.Timeout == 0 {
		cloned.Timeout = timeout
	}
	return &cloned
}
