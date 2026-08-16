package responses

import (
	"context"
	"net/http"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

func (c *Client) post(ctx context.Context, body []byte, operation string) (*http.Response, error) {
	request, err := backendhttp.NewJSONSSERequest(ctx, c.endpoint, body)
	if err != nil {
		return nil, c.errorf("create %s: %v", operation, err)
	}
	if c.prepareRequest != nil {
		if err := c.prepareRequest(ctx, request); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
	}
	return c.errorConfig.Do(ctx, c.httpClient, request, operation)
}

func (c *Client) errorf(format string, arguments ...any) error {
	return c.errorConfig.Errorf(format, arguments...)
}

func (c *Client) wrapf(cause error, format string, arguments ...any) error {
	return c.errorConfig.Wrapf(cause, format, arguments...)
}

func (c *Client) retryableWrapf(cause error, format string, arguments ...any) error {
	return c.errorConfig.RetryableWrapf(cause, format, arguments...)
}
