package chatcompletions

import backendhttp "github.com/eul-ai/eul/backend/httpclient"

type apiError = backendhttp.APIError

type responseFailureError struct {
	message string
	detail  apiError
}

func (err *responseFailureError) Error() string { return err.message }

func (client *Client) errorf(format string, arguments ...any) error {
	return client.errorConfig.Errorf(format, arguments...)
}
