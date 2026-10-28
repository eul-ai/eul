package opencodego

import (
	"context"
	"errors"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type liveModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (account account) loadLiveModels(ctx context.Context) (map[string]struct{}, error) {
	var result liveModelsResponse
	if err := backendhttp.GetJSON(ctx, account.client, account.baseURL+"/models", account.authorize, &result); err != nil {
		return nil, err
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
