package openrouter

import (
	"context"
	"net/http"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type keyResponse struct {
	Data keyUsage `json:"data"`
}

type keyUsage struct {
	MonthlyUsage   float64  `json:"usage_monthly"`
	LimitRemaining *float64 `json:"limit_remaining"`
}

func (configured *runtime) loadKey(ctx context.Context) (keyResponse, error) {
	var result keyResponse
	err := backendhttp.GetJSON(ctx, configured.credentialClient, configured.baseURL+"/key", func(request *http.Request) {
		request.Header.Set("Authorization", "Bearer "+configured.apiKey)
		setCommonHeaders(request)
	}, &result)
	return result, err
}

func (configured *runtime) loadModelCatalog(ctx context.Context) (modelCatalog, error) {
	var result modelCatalog
	err := backendhttp.GetJSON(ctx, configured.credentialClient, configured.baseURL+"/models", setCommonHeaders, &result)
	return result, err
}
