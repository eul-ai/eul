package opencodego

import (
	"net/http"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

func responseError(response *http.Response) error {
	return backendhttp.ReadHTTPStatusError(response, backendhttp.DefaultErrorResponseBytes)
}
