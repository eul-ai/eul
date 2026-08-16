package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
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
	request, err := configured.newGETRequest(ctx, "/key")
	if err != nil {
		return keyResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+configured.apiKey)

	body, err := configured.doGET(request)
	if err != nil {
		return keyResponse{}, err
	}

	var result keyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return keyResponse{}, err
	}
	return result, nil
}

func (configured *runtime) loadModelCatalog(ctx context.Context) (modelCatalog, error) {
	request, err := configured.newGETRequest(ctx, "/models")
	if err != nil {
		return modelCatalog{}, err
	}

	body, err := configured.doGET(request)
	if err != nil {
		return modelCatalog{}, err
	}

	var result modelCatalog
	if err := json.Unmarshal(body, &result); err != nil {
		return modelCatalog{}, err
	}
	return result, nil
}

func (configured *runtime) newGETRequest(ctx context.Context, path string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	setCommonHeaders(request)
	return request, nil
}

func (configured *runtime) doGET(request *http.Request) ([]byte, error) {
	response, err := configured.credentialClient.Do(request)
	if err != nil {
		if contextErr := request.Context().Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, backendhttp.ReadHTTPStatusError(response, backendhttp.DefaultErrorResponseBytes)
	}

	body, truncated, err := backendhttp.ReadBounded(response.Body, backendhttp.DefaultResponseBytes)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("response exceeds %d bytes", backendhttp.DefaultResponseBytes)
	}
	return body, nil
}
