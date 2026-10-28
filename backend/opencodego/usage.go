package opencodego

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type usageResponse struct {
	Usage struct {
		Rolling usageWindow `json:"rolling"`
		Weekly  usageWindow `json:"weekly"`
		Monthly usageWindow `json:"monthly"`
	} `json:"usage"`
}

type usageWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	if _, err := configured.loadUsage(ctx); err != nil {
		return fmt.Errorf("opencode go: validate API key and subscription: %w", err)
	}
	return nil
}

func (configured *runtime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	usage, err := configured.loadUsage(ctx)
	if err != nil {
		return backend.AccountUsage{}, fmt.Errorf("opencode go: load usage: %w", err)
	}

	windows := make([]backend.UsageWindow, 0, 3)
	for _, item := range []struct {
		duration time.Duration
		window   usageWindow
	}{
		{duration: 5 * time.Hour, window: usage.Usage.Rolling},
		{duration: 7 * 24 * time.Hour, window: usage.Usage.Weekly},
		{duration: 30 * 24 * time.Hour, window: usage.Usage.Monthly},
	} {
		resetsAt, err := time.Parse(time.RFC3339, item.window.ResetsAt)
		if err != nil {
			return backend.AccountUsage{}, fmt.Errorf("invalid usage reset time %q: %w", item.window.ResetsAt, err)
		}
		percent := int(math.Round(item.window.Percent))
		percent = max(0, min(percent, 100))
		windows = append(windows, backend.UsageWindow{
			Duration:    item.duration,
			UsedPercent: percent,
			ResetsAt:    resetsAt,
		})
	}
	return backend.AccountUsage{Windows: windows}, nil
}

func (configured *runtime) loadUsage(ctx context.Context) (usageResponse, error) {
	var result usageResponse
	err := backendhttp.GetJSON(ctx, configured.credentialClient, configured.baseURL+"/usage", configured.authorize, &result)
	return result, err
}
