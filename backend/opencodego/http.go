package opencodego

import (
	"net/http"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const maxErrorResponseBytes = int64(64 * 1024)

func responseError(response *http.Response) error {
	return backendhttp.ReadHTTPStatusError(response, maxErrorResponseBytes)
}
