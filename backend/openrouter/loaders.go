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

func (account account) loadKey(ctx context.Context) (keyResponse, error) {
	var result keyResponse
	err := backendhttp.GetJSON(ctx, account.client, account.baseURL+"/key", func(request *http.Request) {
		request.Header.Set("Authorization", "Bearer "+account.apiKey)
		setCommonHeaders(request)
	}, &result)
	return result, err
}

func (account account) loadModelCatalog(ctx context.Context) (modelCatalog, error) {
	var result modelCatalog
	err := backendhttp.GetJSON(ctx, account.client, account.baseURL+"/models", setCommonHeaders, &result)
	return result, err
}
