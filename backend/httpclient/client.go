package httpclient

import (
	"net/http"
	"time"
)

const (
	DefaultGenerationHTTPTimeout       = 10 * time.Minute
	MaxRequestBytes              int64 = 32 * 1024 * 1024
	MaxResponseBytes             int64 = 16 * 1024 * 1024
	maxErrorBytes                int64 = 64 * 1024
)

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
