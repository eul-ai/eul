package httpclient

import (
	"net/http"
	"time"
)

func CloneNoRedirects(source *http.Client, timeout time.Duration) *http.Client {
	client := &http.Client{}
	if source != nil {
		*client = *source
	}
	if timeout > 0 && client.Timeout <= 0 {
		client.Timeout = timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}
