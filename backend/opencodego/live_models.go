package opencodego

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const maxLiveModelsResponseBytes = int64(1024 * 1024)

type liveModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (configured *runtime) loadLiveModels(ctx context.Context) (map[string]struct{}, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if err := prepareBearerRequest(configured.apiKey)(ctx, request); err != nil {
		return nil, err
	}

	response, err := configured.credentialClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseError(response)
	}

	var result liveModelsResponse
	truncated, err := backendhttp.DecodeBoundedJSON(response.Body, maxLiveModelsResponseBytes, &result)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("response exceeds %d bytes", maxLiveModelsResponseBytes)
	}

	models := make(map[string]struct{}, len(result.Data))
	for _, model := range result.Data {
		if model.ID != "" {
			models[model.ID] = struct{}{}
		}
	}
	if len(models) == 0 {
		return nil, errors.New("response contains no models")
	}
	return models, nil
}
