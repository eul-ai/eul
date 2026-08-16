package chatcompletions

import (
	"context"
	"net/http"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type apiError = backendhttp.APIError

type responseFailureError struct {
	message string
	detail  apiError
}

func (err *responseFailureError) Error() string { return err.message }

func (client *Client) post(ctx context.Context, body []byte, operation string) (*http.Response, error) {
	request, err := backendhttp.NewJSONSSERequest(ctx, client.endpoint, body)
	if err != nil {
		return nil, client.errorf("create %s: %v", operation, err)
	}
	if client.prepareRequest != nil {
		if err := client.prepareRequest(ctx, request); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
	}
	return client.errorConfig.Do(ctx, client.httpClient, request, operation)
}

func (client *Client) errorf(format string, arguments ...any) error {
	return client.errorConfig.Errorf(format, arguments...)
}

func (client *Client) wrapf(cause error, format string, arguments ...any) error {
	return client.errorConfig.Wrapf(cause, format, arguments...)
}

func (client *Client) retryableWrapf(cause error, format string, arguments ...any) error {
	return client.errorConfig.RetryableWrapf(cause, format, arguments...)
}
